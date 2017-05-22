#!/bin/bash
sudo chmod +x migucontroller
sudo mkdir -p /var/run/secrets/kubernetes.io/serviceaccount/

sudo cp  ca.crt /var/run/secrets/kubernetes.io/serviceaccount/
sudo chown -R abservice:abservice /var/run/secrets