export default {
  caps: {
    summary: {
      local: 'Local disk: simple; traffic usually streams via the app',
      s3: 'S3-compatible: CDN offload and private presigned GET when configured',
      webdav: 'WebDAV: good for netdisks/panels and OpenList proxies; limited features',
      ftp: 'FTP compatibility: only when the vendor offers FTP and you cannot run a proxy',
    },
  },
  loss: {
    no_presign: 'No private object presign; private images stream through the app',
    cdn_not_typical: 'Public original CDN 302 is atypical; set CDN domain only if you know the risk',
    no_cdn_offload: 'No reliable public CDN offload',
    hot_path: 'Not suitable for hot high-QPS reads; higher latency and connection cost',
    ftp_security: 'FTP may be plaintext; prefer FTPS or move to S3/WebDAV/OpenList',
    ftp_reliability: 'Passive mode, firewalls, and reconnects make ops fragile',
    vendor_semantics: 'Semantics depend on the remote server (mkdir, quotas)',
  },
  help: {
    ftpPreferProxy:
      'Prefer OpenList/rclone to expose FTP as WebDAV or sync to local/S3, then use this service. Built-in FTP is a limited compatibility tier.',
  },
}
