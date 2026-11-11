export default {
  caps: {
    summary: {
      local: '本地磁盘：简单可靠，流量默认走应用进程',
      s3: 'S3 兼容：可配置 CDN 卸带宽与私密预签名直连',
      webdav: 'WebDAV：适合网盘/面板与 OpenList 等代理出口，能力受限',
      ftp: 'FTP 兼容层：仅当供应商只提供 FTP 且无法外置代理时使用',
    },
  },
  loss: {
    no_presign: '不支持私密图对象存储预签名；私密访问经应用流式代理',
    cdn_not_typical: '公开原图 CDN 302 非典型能力；请谨慎填写 CDN 域名',
    no_cdn_offload: '不支持可靠的公开图 CDN 卸带宽',
    hot_path: '不适合高并发热读；远程协议延迟与连接开销较高',
    ftp_security: 'FTP 可能明文传输；优先 FTPS，或迁移到 S3/WebDAV/OpenList',
    ftp_reliability: '被动模式、防火墙与断线重试使运维更脆弱',
    vendor_semantics: '语义依赖远端实现（目录创建、配额），行为可能因服务商而异',
  },
  help: {
    ftpPreferProxy:
      '优先用 OpenList/rclone 等将 FTP 转为 WebDAV 或同步到本地/S3，再接入本服务。内置 FTP 为功能受限兼容层。',
  },
}
