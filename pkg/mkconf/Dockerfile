ARG EVE_BUILDER_IMAGE=lfedge/eve-alpine:7d3b7134bb4ad672a85f0bf71822c9db80724983
FROM ${EVE_BUILDER_IMAGE} AS build

ENV PKGS mtools dosfstools
RUN eve-alpine-deploy.sh

COPY make-config /out/
RUN mkdir -p /out/conf/raw

FROM scratch
COPY --from=build /out /

WORKDIR /
ENTRYPOINT [ "/make-config" ]
