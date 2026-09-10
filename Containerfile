# yasrt — the release tool, packaged for the CI image it runs in.
#
# Wolfi, not distroless: every golden image in this estate descends from
# wolfi-base, and the image needs a real git binary anyway — yasrt drives git
# through os/exec precisely so that signing and credential handling are git's,
# not a library's approximation of it.
FROM registry.ole-hartwig.eu/devops/ci-mirrors/wolfi-base:latest

# renovate: datasource=custom.wolfi depName=git
ARG GIT_APK_VERSION="2.51.0-r0"

USER root
RUN echo "https://packages.wolfi.dev/os" > /etc/apk/repositories \
 && apk add --no-cache \
      "git=~${GIT_APK_VERSION}" \
      gnupg \
      ca-certificates-bundle \
 && update-ca-certificates

COPY dist/yasrt-linux-${TARGETARCH:-arm64} /usr/local/bin/yasrt
RUN chmod 0755 /usr/local/bin/yasrt

# Prove the binary runs before anyone depends on the image. The estate has
# shipped silently broken images before; a smoke test costs one layer.
RUN yasrt version && git --version && gpg --version >/dev/null

# The runner mounts CI_PROJECT_DIR and git refuses to operate on a checkout it
# does not own, so declare it safe rather than making every job do it.
RUN git config --system --add safe.directory '*'

# Non-root by default. Jobs that need to write to the checkout run as the
# runner's own user anyway; nothing here needs root.
RUN adduser -D -u 1000 yasrt
USER 1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/yasrt"]
CMD ["--help"]
