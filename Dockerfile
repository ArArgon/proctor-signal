FROM golang:1.19 AS compile
RUN --mount=type=cache,target=/root/.cache/go-build \
  go build ...

WORKDIR /build

COPY ./ ./

SHELL ["/bin/bash", "-c"]

RUN go version

RUN env

RUN if [[ $BUILD_LOCATION = "cn" ]] ; then \
      echo using goproxy.cn; \
      go env -w GOPROXY=https://goproxy.cn,direct; \
    fi

RUN go mod tidy

RUN go build proctor-signal/cmd/proctor

FROM ubuntu:20.04

WORKDIR /app

SHELL ["/bin/bash", "-c"]

RUN rm -f /etc/apt/apt.conf.d/docker-clean; echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache

RUN if [[ $BUILD_LOCATION = "cn" ]] ; then \
      echo replacing original apt repository to tuna; \
      sed -i "s@http://.*archive.ubuntu.com@https://mirrors.tuna.tsinghua.edu.cn@g" /etc/apt/sources.list;\
      sed -i "s@http://.*security.ubuntu.com@https://mirrors.tuna.tsinghua.edu.cn@g" /etc/apt/sources.list;\
    fi

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt update && \
    apt-get install -y ca-certificates && \
    add-apt-repository ppa:deadsnakes/ppa && \
    apt update && \
    apt-get --no-install-recommends install -y gcc g++ python3.8 python3.9 openjdk-17-jdk

COPY --from=compile /build/proctor ./
COPY --from=compile /build/conf ./conf

RUN ls -al

ENTRYPOINT ["./proctor"]