# yasrt — the release tool, packaged for the CI job it runs in.
#
# Wolfi, not distroless: every golden image in this estate descends from
# wolfi-base, and the image needs a real git binary anyway — yasrt drives git
# through os/exec precisely so that signing, credential handling and
# shallow-clone behaviour are git's own, not a library's approximation.
#
# renovate: datasource=docker depName=registry.ole-hartwig.eu/devops/ci-mirrors/wolfi-base
FROM registry.ole-hartwig.eu/devops/ci-mirrors/wolfi-base:latest@sha256:a31344ab2cb8618db84f535eec56f76f6178b142cb92cb2e48676cc2dcebea72

# Set by BuildKit for each leg of a multi-platform build.
ARG TARGETARCH

# renovate: datasource=custom.wolfi depName=git
ARG GIT_APK_VERSION="2.51.0-r0"

USER root

# Address selection before the first apk call (estate rule K38). The build
# fleet has no public IPv4; without this, apk tries the A records first, waits
# out a sixty-second timeout per step and only then falls back. The build stays
# green and merely takes twenty minutes longer, which is how it went unnoticed
# in ten images for eleven weeks. Copied verbatim from devops/images/wolfi-base;
# note that a heredoc here is parsed as a LABEL instruction by some tooling, so
# printf it is.
RUN printf 'label     ::1/128       0\nlabel     ::/0          1\nlabel     ::ffff:0:0/96 4\nprecedence ::1/128       50\nprecedence ::/0          40\nprecedence ::ffff:0:0/96 10\n' > /etc/gai.conf

# One layer: the packages, the trust store, and the smoke test that proves the
# tools are actually there. The estate has shipped silently broken images
# before; checking costs nothing here and catches it at build time.
RUN echo "https://packages.wolfi.dev/os" > /etc/apk/repositories \
 && apk add --no-cache \
      "git=~${GIT_APK_VERSION}" \
      gnupg \
      ca-certificates-bundle \
 && update-ca-certificates \
 && git --version \
 && gpg --version > /dev/null

COPY dist/yasrt-linux-${TARGETARCH} /usr/local/bin/yasrt

# The runner mounts CI_PROJECT_DIR with a different owner than the job user and
# git then refuses to touch it, so declare it safe once rather than in every
# consuming job. Non-root: nothing yasrt does needs more.
RUN chmod 0755 /usr/local/bin/yasrt \
 && yasrt version \
 && git config --system --add safe.directory '*' \
 && adduser -D -u 1000 yasrt

USER 1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/yasrt"]
CMD ["--help"]
