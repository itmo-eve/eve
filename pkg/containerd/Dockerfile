ARG GOVER=1.15.3
FROM golang:${GOVER}-alpine as build

RUN apk add --no-cache btrfs-progs-dev gcc libc-dev linux-headers make libseccomp-dev git

# checkout and compile containerd
# Update `FROM` in `pkg/containerd/Dockerfile`, `pkg/init/Dockerfile` and
# `test/pkg/containerd/Dockerfile` when changing this.
ENV CONTAINERD_REPO=https://github.com/containerd/containerd.git
ENV CONTAINERD_COMMIT=v1.4.1
RUN mkdir -p $GOPATH/src/github.com/containerd && \
  cd $GOPATH/src/github.com/containerd && \
  git clone https://github.com/containerd/containerd.git && \
  cd $GOPATH/src/github.com/containerd/containerd && \
  git checkout $CONTAINERD_COMMIT
WORKDIR $GOPATH/src/github.com/containerd/containerd
RUN make binaries EXTRA_FLAGS="-buildmode pie" EXTRA_LDFLAGS='-extldflags "-fno-PIC -static -s -w"' BUILDTAGS="static_build no_devmapper"

WORKDIR $GOPATH/src/github.com/containerd/containerd
RUN strip bin/containerd bin/ctr bin/containerd-shim bin/containerd-shim-runc-v2
RUN cp bin/containerd bin/ctr bin/containerd-shim bin/containerd-shim-runc-v2 /usr/bin/

RUN mkdir -p /etc/init.d && ln -s /usr/bin/service /etc/init.d/020-containerd

COPY etc/containerd/* /etc/containerd/

FROM scratch
ENTRYPOINT []
WORKDIR /
COPY --from=build /usr/bin/containerd /usr/bin/ctr /usr/bin/containerd-shim /usr/bin/containerd-shim-runc-v2 /usr/bin/
COPY --from=build /etc/containerd/config.toml /etc/containerd/
COPY --from=build /etc/init.d/ /etc/init.d/
COPY etc etc/