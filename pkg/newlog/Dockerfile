FROM lfedge/eve-alpine:7d3b7134bb4ad672a85f0bf71822c9db80724983 as build
ENV BUILD_PKGS libc-dev git gcc linux-headers go
ENV PKGS alpine-baselayout musl-utils
RUN eve-alpine-deploy.sh

COPY ./  /newlog/.
WORKDIR /newlog

RUN GO111MODULE=on CGO_ENABLED=1 go build -mod=vendor -o newlogd ./cmd
RUN strip newlogd
RUN cp newlogd /out/usr/bin

FROM scratch
COPY --from=build /out/ /
COPY newlogd-init.sh /newlogd-init.sh

WORKDIR /newlog
ENTRYPOINT []
CMD ["/newlogd-init.sh"]
