#!/usr/bin/env bash
# 分片上传端到端检查：起一个本地镜像，按文件大小自动走单请求或 session 分片。
#
#   docker build -t imghk:e2e .
#   scripts/e2e-chunked-upload.sh imghk:e2e big.png [big250.png ...]
#
# 覆盖：注册 → 放宽用户组上限 → 小文件单请求回归 → 取消会话 → 分片（先传 2 片、
# 查续传状态、其余乱序 3 路并发、complete、直链逐字节比对、缩略图、会话目录清理）。
#
# 只在 Linux / WSL 上跑得动：需要 docker、curl、python3、split、shuf。
# 数据目录默认 /var/tmp 而不是 /tmp —— WSL 的 /tmp 是 2 GiB tmpfs，放不下 250MB 级文件。
set -euo pipefail

IMG=${1:?用法: e2e-chunked-upload.sh <镜像> <文件> [文件...]}
shift
[ $# -gt 0 ] || { echo "至少给一个图片文件" >&2; exit 2; }

PORT=${PORT:-8701}
BASE="http://127.0.0.1:${PORT}"
DATA=${DATA:-/var/tmp/imghk-chunk-data}
CHUNK_SIZE_EXPECTED=${CHUNK_SIZE_EXPECTED:-8388608}   # 与 uploadChunkSize 一致
SMALL_MAX=$((32 << 20))                               # 与前端 CHUNK_THRESHOLD 一致

JAR=$(mktemp)
WORK=$(mktemp -d -p "$(dirname "$DATA")")
trap 'docker rm -f imgli-e2e >/dev/null 2>&1 || true; rm -f "$JAR"; rm -rf "$WORK"' EXIT

die() { echo "FAIL: $*" >&2; exit 1; }
jget() { python3 -c "import sys,json;print(json.load(sys.stdin)$1)"; }

echo "== 启动 $IMG（$BASE，数据 $DATA）"
docker rm -f imgli-e2e >/dev/null 2>&1 || true
rm -rf "$DATA" && mkdir -p "$DATA"
docker run -d --name imgli-e2e -p "127.0.0.1:${PORT}:8686" --memory=2g --cpus=2 \
  -e IMGLI_LISTEN=:8686 -e IMGLI_DATA_DIR=/data \
  -e "IMGLI_BASE_URL=${BASE}" -e IMGLI_TRUST_PROXY=false \
  -e VIPS_CONCURRENCY=1 \
  -v "$DATA:/data" "$IMG" >/dev/null

for _ in $(seq 1 60); do
  curl -fsS "$BASE/healthz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "$BASE/healthz" >/dev/null || die "server 未就绪，看 docker logs imgli-e2e"

echo "== 注册测试用户并放宽用户组上限"
curl -fsS -c "$JAR" -H 'Content-Type: application/json' \
  -d '{"username":"tester","email":"tester@example.com","password":"e2e-only-password-1"}' \
  "$BASE/api/v1/auth/register" >/dev/null
curl -fsS -b "$JAR" -X PATCH -H 'Content-Type: application/json' \
  -d '{"max_file_size":268435456,"storage_quota":107374182400}' \
  "$BASE/api/v1/admin/groups/1" >/dev/null

put_chunk() { # sid idx path
  local sid="$1" idx="$2" path="$3" code
  code=$(curl -sS -b "$JAR" -X PUT --data-binary "@${path}" \
    -o /dev/null -w '%{http_code}' "$BASE/api/v1/upload/session/${sid}/${idx}")
  [ "$code" = "200" ] || die "PUT chunk ${idx} -> ${code}"
}

single() { # file  —— 小文件走原来的单请求路径，确认没有被改动波及
  local file="$1" resp key
  resp=$(curl -fsS -b "$JAR" -F "file=@${file}" -F visibility=public -F album_id=0 \
    "$BASE/api/v1/upload")
  key=$(jget '["data"]["key"]' <<<"$resp")
  echo "   key=${key} size=$(jget '["data"]["size"]' <<<"$resp")"
}

chunked() { # file
  local file="$1" name size ext resp sid cs total dir n t_start t_end
  t_start=$(date +%s%N)
  name=$(basename "$file")
  ext=${name##*.}
  size=$(stat -c%s "$file")

  resp=$(curl -fsS -b "$JAR" -H 'Content-Type: application/json' \
    -d "{\"name\":\"${name}\",\"size\":${size},\"visibility\":\"public\",\"album_id\":0}" \
    "$BASE/api/v1/upload/session")
  sid=$(jget '["data"]["session_id"]' <<<"$resp")
  cs=$(jget '["data"]["chunk_size"]' <<<"$resp")
  total=$(jget '["data"]["chunks_total"]' <<<"$resp")
  echo "== ${name}: ${size} bytes -> session ${sid}, chunk ${cs}, total ${total}"
  [ "$total" -gt 1 ] || die "期望多分片"
  [ "$cs" -eq "$CHUNK_SIZE_EXPECTED" ] || die "chunk_size 预期 ${CHUNK_SIZE_EXPECTED}，实际 ${cs}"

  dir="${WORK}/${name}.parts"
  mkdir -p "$dir"
  split -b "$cs" -d -a 5 "$file" "$dir/p-"
  n=$(ls "$dir" | wc -l)
  [ "$n" -eq "$total" ] || die "split 片数 ${n} != ${total}"

  # 先故意只传中间两片，模拟中断，再查续传状态。
  put_chunk "$sid" 1 "$dir/p-00001"
  put_chunk "$sid" 0 "$dir/p-00000"
  resp=$(curl -fsS -b "$JAR" "$BASE/api/v1/upload/session/${sid}")
  jget '["data"]["missing"]' <<<"$resp" | grep -q '\[2,' || die "续传状态 missing 不合预期: $resp"
  echo "   续传状态: received=$(jget '["data"]["received_bytes"]' <<<"$resp") missing=$(jget '["data"]["missing"]' <<<"$resp")"

  # 其余分片乱序、3 路并发。
  local codes ok expected
  expected=$((total - 2))
  codes=$(find "$dir" -maxdepth 1 -type f -name 'p-*' | grep -vE 'p-0000[01]$' | shuf | \
    xargs -P3 -I{} bash -c \
    'idx=$((10#$(basename "$1" | sed "s/^p-//"))); curl -sS -b "$2" -X PUT --data-binary "@$1" -o /dev/null -w "%{http_code}\n" "$3/api/v1/upload/session/$4/$idx"' \
    _ {} "$JAR" "$BASE" "$sid")
  ok=$(grep -c '^200$' <<<"$codes" || true)
  [ "$ok" -eq "$expected" ] || die "并发分片成功 ${ok}/${expected}: ${codes}"

  resp=$(curl -fsS -b "$JAR" "$BASE/api/v1/upload/session/${sid}")
  [ "$(jget '["data"]["missing"]' <<<"$resp")" = "[]" ] || die "仍有缺失分片: $resp"
  echo "   全部分片到位，开始 complete"

  local t0 t1 key
  t0=$(date +%s%N)
  resp=$(curl -fsS -b "$JAR" -X POST "$BASE/api/v1/upload/session/${sid}/complete")
  t1=$(date +%s%N)
  key=$(jget '["data"]["key"]' <<<"$resp")
  [ "$(jget '["data"]["size"]' <<<"$resp")" = "$size" ] || die "返回 size 不符: $resp"
  echo "   complete 用时 $(( (t1 - t0) / 1000000 )) ms -> key=${key}"

  curl -fsS "$BASE/i/${key}.${ext}" -o "$WORK/dl-${name}"
  cmp -s "$file" "$WORK/dl-${name}" || die "直链内容与原文件不一致"
  echo "   直链字节一致 ✔"

  local tcode
  tcode=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/t/${key}.jpg")
  [ "$tcode" = "200" ] || die "缩略图 ${tcode}"
  echo "   缩略图 200 ✔"

  if docker exec imgli-e2e sh -c "ls -A /data/.upload-sessions | grep -q '^${sid}$'" 2>/dev/null; then
    die "会话目录未清理"
  fi
  echo "   会话目录已清理 ✔"

  t_end=$(date +%s%N)
  echo "   端到端总耗时 $(( (t_end - t_start) / 1000000 )) ms（切片+传输+complete+校验）"
}

echo "== 取消会话"
resp=$(curl -fsS -b "$JAR" -H 'Content-Type: application/json' \
  -d '{"name":"cancel.png","size":8388608,"visibility":"public","album_id":0}' \
  "$BASE/api/v1/upload/session")
csid=$(jget '["data"]["session_id"]' <<<"$resp")
curl -fsS -b "$JAR" -X DELETE "$BASE/api/v1/upload/session/${csid}" >/dev/null
code=$(curl -sS -b "$JAR" -o /dev/null -w '%{http_code}' "$BASE/api/v1/upload/session/${csid}")
[ "$code" = "404" ] || die "取消后 GET 应 404，实际 ${code}"
echo "   取消后 GET 404 ✔"

for f in "$@"; do
  [ -f "$f" ] || die "文件不存在: $f"
  size=$(stat -c%s "$f")
  if [ "$size" -ge "$SMALL_MAX" ]; then
    chunked "$f"
  else
    echo "== $(basename "$f"): ${size} bytes（< 32MiB，走单请求）"
    single "$f"
  fi
done

echo "== docker logs (tail)"
docker logs --tail 15 imgli-e2e
echo "ALL OK"
