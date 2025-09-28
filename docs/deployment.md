# 部署指南：跨主机 Docker 使用

本文档说明如何在一台主机上构建 Seekfile 的 Docker 镜像，并将其复制到另一台机器上运行。

## 先决条件

- 构建机和运行机都已安装 Docker 25+（或兼容版本）。
- 两台机器的 CPU 架构相同（下文假设为 `linux/amd64`）。如需跨架构部署，可以在构建时指定 `--platform` 并确保运行机支持该架构的镜像。
- 有足够的磁盘空间保存镜像导出文件（镜像约数百 MB）。

## 1. 在构建机上准备源代码

```bash
# 假设仓库已经克隆至当前目录
cd path/to/seekfile
```

如需自定义配置文件，可在仓库外单独准备一个目录，例如 `config/seekfile.config.json`，内容如下：

```json
{
  "listen_addr": ":8080",
  "scan_paths": ["/data"],
  "rebuild_on_start": false,
  "database_path": "/config/seekfile.db"
}
```

## 2. 构建 Docker 镜像

在仓库根目录执行：

```bash
docker build -t seekfile:offline .
```

这条命令会使用仓库中的 `Dockerfile` 构建 `seekfile:offline` 镜像。构建过程中会下载依赖并编译 Go 二进制，生成的镜像已包含运行所需的可执行文件。

> 如需指定其他平台，例如 `linux/arm64`，可以在命令中加上 `--platform linux/arm64`。

## 3. 导出镜像并传输

构建完成后，将镜像保存为 tar 包并复制到目标机器：

```bash
# 导出镜像为 tar 文件
docker save seekfile:offline -o seekfile-offline.tar

# 通过 scp 或其他方式复制到运行机
scp seekfile-offline.tar user@target-host:/path/to/seekfile-offline.tar
```

如果目标机器无法直接从构建机拉取文件，也可以使用移动存储介质或公司内部制品库。

## 4. 在运行机上导入并启动容器

1. 导入镜像：

   ```bash
   docker load -i /path/to/seekfile-offline.tar
   ```

   导入完成后，运行机上会出现与构建机相同标签的镜像：

   ```bash
   docker images | grep seekfile
   ```

2. 准备挂载目录：

   ```bash
   mkdir -p /srv/seekfile/config /srv/seekfile/data
   # 将配置文件复制到 /srv/seekfile/config/seekfile.config.json
   ```

3. 启动容器：

   ```bash
   docker run -d \
     --name seekfile \
     -p 8080:8080 \
     -v /srv/seekfile/data:/data:ro \
     -v /srv/seekfile/config:/config \
     seekfile:offline
   ```

   - `-p 8080:8080` 将容器端口映射到宿主机端口，访问 `http://运行机IP:8080/` 即可使用 Web UI。
   - `-v /srv/seekfile/data:/data:ro` 挂载需要索引的目录，只读模式确保文件安全；如需写入可移除 `:ro`。
   - `-v /srv/seekfile/config:/config` 提供配置文件与 SQLite 缓存的持久化存储。

4. 查看运行状态：

   ```bash
   docker logs -f seekfile
   ```

   日志中出现 `seekfile listening on :8080` 表示服务启动成功。

## 5. 更新镜像的建议流程

当源代码或配置发生变更时，可以在构建机重复步骤 2 至 4：重新构建镜像、导出、传输并在运行机上使用 `docker load` 覆盖旧镜像，再通过 `docker stop` + `docker rm` + `docker run` 或 `docker container update` 重新启动容器以应用新版本。

> 若频繁更新，可考虑使用私有镜像仓库推送镜像，在运行机上直接 `docker pull` 获取最新版本，以减少手动导出/导入操作。

