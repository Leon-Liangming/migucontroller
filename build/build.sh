#!/bin/bash

ROOT=$(cd `dirname $0`; pwd)
SRC_DIR=$ROOT/../
cd $SRC_DIR
godep go build

#clear release file
rm -rf paas-controller
rm migucontroller.tar.gz

mkdir -p paas-controller
cp migucontroller ca.crt upstream.tmpl ./deploy/install.sh ./deploy/start.sh ./paas-controller

tar zcf migucontroller.tar.gz ./paas-controller

mv migucontroller.tar.gz $ROOT
rm -rf ./paas-controller