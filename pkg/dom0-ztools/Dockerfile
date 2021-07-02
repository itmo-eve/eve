ARG EVE_BUILDER_IMAGE=lfedge/eve-alpine:7d3b7134bb4ad672a85f0bf71822c9db80724983
# hadolint ignore=DL3006
FROM ${EVE_BUILDER_IMAGE} as zfs
ENV PKGS zfs ca-certificates util-linux
RUN eve-alpine-deploy.sh

FROM scratch
COPY --from=zfs /out/ /
ADD rootfs/ /
