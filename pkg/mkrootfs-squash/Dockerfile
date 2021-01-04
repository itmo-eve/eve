FROM alpine:3.12 AS make-img

RUN \
  apk add --no-cache \
  dosfstools=4.1-r1 \
  libarchive-tools=3.4.3-r1 \
  binutils=2.34-r1 \
  mtools=4.0.24-r0 \
  sfdisk=2.35.2-r0 \
  sgdisk=1.0.5-r0 \
  xfsprogs=5.6.0-r1 \
  e2fsprogs=1.45.6-r0 \
  util-linux=2.35.2-r0 \
  coreutils=8.32-r0 \
  multipath-tools=0.8.4-r1 \
  squashfs-tools=4.4-r0

COPY . .

ENTRYPOINT [ "/make-rootfs" ]
