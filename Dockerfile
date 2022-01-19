FROM photon:2.0

MAINTAINER wangyijun <wangyijun@xlauncher.io>

COPY lsh-cluster-addon-controller /lsh-cluster-addon-controller

ENTRYPOINT [ "/lsh-cluster-addon-controller" ]
