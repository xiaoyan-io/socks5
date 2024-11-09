#!/usr/bin/env node
//@DNetL
const cpath = './config.json';
const fs = require('fs');
const net = require('net');
const https = require('https');
const { WebSocket, createWebSocketStream } = require('ws');
const logcb = (...args) => console.log.bind(this, ...args);
const errcb = (...args) => console.error.bind(this, ...args);
const opts = {}, cache = {};

const CFDOMAN = [];
const CIDR4 = ['173.245.48.0/20', '103.21.244.0/22', '103.22.200.0/22', '103.31.4.0/22', '141.101.64.0/18', '108.162.192.0/18', '190.93.240.0/20',
	'188.114.96.0/20', '197.234.240.0/22', '198.41.128.0/17', '162.158.0.0/15', '104.16.0.0/13', '104.24.0.0/14', '172.64.0.0/13', '131.0.72.0/22'];
const CIDR6 = ['2400:cb00::/32', '2606:4700::/32', '2803:f800::/32', '2405:b500::/32', '2405:8100::/32', '2a06:98c0::/29', '2c0f:f248::/32'];

const ADDR4 = CIDR4.map(cidr => {
	const [addr, mask] = cidr.split('/');
	return { m: (0xffffffff << 32 - Number(mask)) >>> 0, a: addr.split('.').map(Number).reduce((s, b, i) => s += (b << 24 - 8 * i), 0) };
});
const ADDR6 = CIDR6.map(cidr => {
	const [addr, mask] = cidr.split('/');
	return { m: mask, s: addr.split(':').map(p => parseInt(p, 16).toString(2).padStart(16, '0')).join('').slice(0, mask) };
});
const ipInCFCidr = ip => {
	if (ip.indexOf(':') == -1) {
		const ipa = ip.split('.').map(Number);
		return { cf: ADDR4.some(({ a, m }) => (ipa.reduce((s, b, i) => s += (b << 24 - 8 * i), 0) & m) === (a & m)), ip: ip };
	} else {
		const ips = ip.split(':').map(p => parseInt(p, 16).toString(2).padStart(16, '0')).join('');
		return { cf: ADDR6.some(({ s, m }) => ips.slice(0, m) === s), ip: ip };
	}
}

const dns = host => new Promise((res, rej) => {
	const o = Object.assign({ method: 'GET', headers: { 'Accept': 'application/dns-json' } }, opts);
	https.request(`https://cloudflare-dns.com/dns-query?name=${host}&type=A`, o, r => {
		let data = '';
		r.on('data', chunk => data += chunk).on('end', () => {
			const d = JSON.parse(data);
			if (d.Status === 0 && d.Answer && d.Answer.length > 0) res(d.Answer.map(a => a.data).find(ip => /^(\d{1,3}\.){3}\d{1,3}$/.test(ip)));
			else rej(new Error('no ipv4 addr'));
		}).end();
	}).on('error', error => rej(error));
});

const isCFIP = (host, ATYP) => new Promise((res, rej) => {
	if (CFDOMAN.includes(host)) res({ cf: true, ip: host });
	else if (cache[host] == undefined) {
		if (ATYP == 0x01 || ATYP == 0x04) res(cache[host] = ipInCFCidr(host));
		else if (ATYP == 0x03) dns(host).then(ip => res(cache[host] = ipInCFCidr(ip))).catch(e => res({ cf: false, ip: host }));
	} else res(cache[host]);
});

const socks = async ({ domain, psw, sport = 1080, sbind = '127.0.0.1', wkip, wkport, proxyip, proxyport, cfhs = [] }) => {
	// Add custom hostnames to the CFDOMAN array
	Object.assign(CFDOMAN, cfhs);

	// Construct the WebSocket URL
	let url = 'wss://' + domain;

	// If wkip is provided, use it to construct the WebSocket URL
	if (wkip) {
		const wsProtocol = 'wss://';
		url = `${wsProtocol}${wkip}${wkport ? `:${wkport}` : ''}`;
		console.log(url);

		// Set the Host header and SNI to ensure proper routing
		Object.assign(opts, {
			headers: {
				...opts.headers,
				'Host': domain
			},
			servername: domain // Add SNI (Server Name Indication)
		});
	}

	// Create a TCP server to handle SOCKS5 connections
	net.createServer(socks => socks.once('data', data => {
		const [VERSION] = data; // Extract SOCKS version from client greeting

		// Check if the client is using SOCKS5 protocol
		if (VERSION != 0x05) socks.end();
		else if (data.slice(2).some(method => method == 0x00)) { // Check if client supports no authentication
			socks.write(Buffer.from([0x05, 0x00])); // Send authentication method choice (no auth)

			// Wait for client's connection request
			socks.once('data', head => {
				const [VERSION, CMD, RSV, ATYP] = head;
				if (VERSION != 0x05 || CMD != 0x01) return; // Ensure SOCKS5 and CONNECT command

				// Parse destination address based on address type (IPv4, IPv6, or Domain)
				const host = ATYP == 0x01 ? head.slice(4, -2).map(b => parseInt(b, 10)).join('.') : // IPv4
					(ATYP == 0x04 ? head.slice(4, -2).reduce((s, b, i, a) => (i % 2 ? s.concat(a.slice(i - 1, i + 1)) : s), []).map(b => b.readUInt16BE(0).toString(16)).join(':') : // IPv6
						(ATYP == 0x03 ? head.slice(5, -2).toString('utf8') : '')); // Domain
				const port = head.slice(-2).readUInt16BE(0);

				// Check if the destination IP is a Cloudflare IP
				isCFIP(host, ATYP).then(({ cf, ip }) => {
					new Promise((res, rej) => {
						if (cf && !proxyip) {
							// If it's a Cloudflare IP and byip is not set, connect directly
							console.log('connect direct');
							const connectPort = port;
							net.connect(connectPort, wkip ? wkip : ip, function () { res(this); }).on('error', rej);
						} else {
							console.log('connect websocket');
							console.log("proxyip", proxyip);
							console.log("proxyport", proxyport);

							// Otherwise, connect through WebSocket
							new WebSocket(url, opts).on('open', function (e) {
								this.send(JSON.stringify({ hostname: cf ? proxyip : ip, port: cf ? proxyport : port, psw }));
								res(createWebSocketStream(this));
							}).on('error', e => rej);
						}
					}).then(s => {
						// Connection successful, send success response to client
						socks.write((head[1] = 0x00, head));
						logcb('conn:')({ host, port, cf });
						// Pipe data between client and destination
						socks.pipe(s).on('error', e => errcb('E1:')(e.message)).pipe(socks).on('error', e => errcb('E2:')(e.message));
					}).catch(e => {
						// Connection failed, send failure response to client
						errcb('connect-catch:')(e.message);
						socks.end((head[1] = 0x03, head));
					});
				});
			});
		} else socks.write(Buffer.from([0x05, 0xff])); // Reject if no supported auth method
	}).on('error', e => errcb('socks-err:')(e.message))
	).listen(sport, sbind, logcb(`server start on: ${sbind}:${sport}`)).on('error', e => errcb('socks5-err')(e.message));
}
fs.exists(cpath, e => {
	if (e) socks(JSON.parse(fs.readFileSync(cpath)));
	else console.error('当前程序的目录没有config.txt文件!');
});
