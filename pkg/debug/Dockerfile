FROM alpine:3.11 as sysdig-build
ENTRYPOINT []
WORKDIR /
ARG KERNELVER=5.4.51
ARG SYSDIGVER=0.27.1
COPY kernel_config-5.4.x-x86_64 /.config
COPY /patches /patches

RUN apk add --no-cache --update git wget ca-certificates \
    build-base gcc abuild binutils linux-headers \
    bc flex bison openssl-dev cmake perl ncurses-dev libc-dev \
    patch elfutils-libelf elfutils-dev musl-dev && \
  export KERNELDIR=/src/linux-$KERNELVER && \
  mkdir /src && \
  cd /src && \
  wget https://www.kernel.org/pub/linux/kernel/v5.x/linux-$KERNELVER.tar.gz && \
  tar zxf linux-$KERNELVER.tar.gz && \
  cd /src/linux-$KERNELVER && \
  cp /.config .config && \
  make modules_prepare && \
  mv .config /src/config && \
  cd /src && \
  wget https://github.com/draios/sysdig/archive/$SYSDIGVER.tar.gz && \
  tar zxf $SYSDIGVER.tar.gz && cd /src/sysdig-$SYSDIGVER && \
  set -e ; [ ! -d /patches ] || for patch in /patches/*.patch; do \
        echo "Applying $patch"; \
        patch -p1 < "$patch"; \
    done && \
  mkdir -p /sysdig/build && \
  cd /sysdig/build && \
  cmake /src/sysdig-$SYSDIGVER -DMUSL_OPTIMIZED_BUILD=ON -DMINIMAL_BUILD=ON && \
  make -j $(getconf _NPROCESSORS_ONLN) && \
  make driver -j $(getconf _NPROCESSORS_ONLN) && \
  rm -rf /src && \
  apk del git wget ca-certificates \
    build-base gcc abuild binutils linux-headers \
    bc flex bison openssl-dev cmake perl ncurses-dev libc-dev \
    patch elfutils-libelf elfutils-dev musl-dev

FROM alpine:3.12

#RUN apk add --no-cache pciutils usbutils vim tcpdump gdb valgrind perf util-linux

COPY --from=sysdig-build /sysdig/build/userspace/sysdig/sysdig /usr/bin/sysdig
COPY --from=sysdig-build /sysdig/build/userspace/sysdig/chisels /sysdig/chisels
COPY --from=sysdig-build /sysdig/build/driver/sysdig-probe.ko /sysdig/sysdig-probe.ko

COPY debug-spin.sh /

CMD ["/debug-spin.sh"]
