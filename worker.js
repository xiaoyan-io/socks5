import { connect } from 'cloudflare:sockets';

// 定义密码，用于验证客户端
let passwd = '123456';

export default {

  async fetch(request, env, _ctx) {
    // 从环境变量中获取密码，如果没有则使用默认密码
    passwd = env.PASSWD || passwd;
    // 检查请求头中是否包含 WebSocket 升级请求
    const upgradeHeader = request.headers.get("Upgrade");

    if (upgradeHeader !== "websocket") return new Response(null, { status: 404 });

    // 创建 WebSocket 对，获取客户端和服务器端的 WebSocket 对象
    const [client, server] = Object.values(new WebSocketPair());

    // 接受 WebSocket 连接
    server.accept();

    // 添加消息事件监听器，只触发一次
    server.addEventListener('message', ({ data }) => {
      try {
        // 解析客户端发送的 JSON 数据
        const { hostname, port, psw } = JSON.parse(data);

        // 验证密码
        if (passwd != psw) throw 'Illegal-User';

        // 创建到目标主机的 TCP 连接
        const socket = connect({ hostname, port });

        // 创建一个 ReadableStream，将 WebSocket 数据传输到 TCP 连接
        new ReadableStream({
          start(controller) {
            // 设置 WebSocket 事件处理器
            server.onmessage = ({ data }) => controller.enqueue(data);
            server.onerror = e => controller.error(e);
            server.onclose = e => controller.close(e);
          },
          cancel(reason) { server.close(); }
        }).pipeTo(socket.writable);

        // 创建一个 WritableStream，将 TCP 连接数据传输到 WebSocket
        socket.readable.pipeTo(new WritableStream({
          start(controller) { server.onerror = e => controller.error(e); },
          write(chunk) { server.send(chunk); }
        }));
      } catch (error) {
        // 如果发生错误，关闭 WebSocket 连接
        server.close();
      }
    }, { once: true });

    // 返回 WebSocket 升级响应
    return new Response(null, { status: 101, webSocket: client });
  }
}