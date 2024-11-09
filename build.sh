#!/bin/bash

# 设置Go程序的名称
APP_NAME="cf-worker-socks5"

# 设置版本号
VERSION="1.0.0"

# 创建一个临时目录来存放编译后的文件
mkdir -p build

# 定义要编译的操作系统和架构
PLATFORMS=("windows/amd64" "windows/386" "darwin/amd64" "darwin/arm64" "linux/amd64" "linux/386" "linux/arm" "linux/arm64")

# 遍历平台列表并编译
for PLATFORM in "${PLATFORMS[@]}"
do
    # 分割操作系统和架构
    IFS='/' read -r -a array <<< "$PLATFORM"
    GOOS=${array[0]}
    GOARCH=${array[1]}
    
    # 设置输出文件名
    OUTPUT_NAME=$APP_NAME'_'$VERSION'_'$GOOS'_'$GOARCH
    
    if [ $GOOS = "windows" ]; then
        OUTPUT_NAME+='.exe'
    fi

    # 编译
    env GOOS=$GOOS GOARCH=$GOARCH go build -o build/$OUTPUT_NAME
    
    if [ $? -ne 0 ]; then
        echo 'An error has occurred! Aborting the script execution...'
        exit 1
    fi
done

# 创建一个zip文件并将所有编译好的文件添加进去
cd build
zip -r ../$APP_NAME'_'$VERSION'.zip' .
cd ..

# 清理build目录
rm -rf build

echo "Cross-compilation completed. Zip file created: $APP_NAME'_'$VERSION'.zip'"
