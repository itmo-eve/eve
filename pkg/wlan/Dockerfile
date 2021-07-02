FROM lfedge/eve-alpine:7d3b7134bb4ad672a85f0bf71822c9db80724983 as build
ENV PKGS alpine-baselayout musl-utils wireless-tools wpa_supplicant
RUN eve-alpine-deploy.sh

FROM scratch
COPY --from=build /out/ /
COPY init.sh /init.sh

ENTRYPOINT []
CMD ["/init.sh"]
