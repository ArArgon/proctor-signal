FROM golang:1.19 AS compile

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

RUN apt update && \
    apt-get install -y ca-certificates

RUN if [[ $BUILD_LOCATION = "cn" ]] ; then \
      echo replacing original apt repository to tuna; \
      sed -i "s@http://.*archive.ubuntu.com@https://mirrors.tuna.tsinghua.edu.cn@g" /etc/apt/sources.list;\
      sed -i "s@http://.*security.ubuntu.com@https://mirrors.tuna.tsinghua.edu.cn@g" /etc/apt/sources.list;\
    fi

RUN apt update && \
    apt-get --no-install-recommends install -y gcc g++ python3 openjdk-11-jdk openjdk-17-jdk openjdk-8-jdk

COPY --from=compile /build/proctor ./
COPY --from=compile /build/conf ./conf

RUN ls -al

ENTRYPOINT ["./proctor"]