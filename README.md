***migucontroller***
=====================
**1、准备配置证书**
---------------------
连接kubernetes apiserver，需要通过https的方式认证，通过CA证书+Token的方式访问。
*    1）提供ca.crt，可以从OM-Core节点的/var/paas/srv/kubernetes中获取，获取后放到/var/run/secrets/kubernetes.io/serviceaccount/目录下
*    2）Token，配置access-key和secret-key即可，可以从部署配置中查找。程序会使用这两个参数从iam中获取token

**1、关键配置项说明**
---------------------
* access-key、secret-key：连接kube-apiserver所需的认证信息，可以从OM-Core节点/var/paas/bootstrap/conf/default_tenant_PMHA.conf文件中查找cfe_controller_iam_accesskey,cfe_controller_iam_secretkey获取,简称aksk
* token-namespace：和access-key、secret-key相关，说明aksk所属的租户，默认为op_svc_cfe
* iam-sersver-address：iam认证服务器的ip:port地址，可以从OM-Core节点/var/paas/bootstrap/conf/default_tenant_PMHA.conf文件中查找获取
* kube-apiserver-ip: manage管理面的kube-apiserver ip地址，可以在OM-Core上通过命令行查询获取（kubectl -n manage get svc | kube-apiserver）
* kube-apiserver-port: manage管理面的kube-apiserver 端口，可以在OM-Core上通过命令行查询获取（kubectl -n manage get svc | kube-apiserver）
* watch-namespace：可以设置需要关注的namespace，默认为监听所有namespace的数据，可以设置只关注某个namespace。
* sync-period：数据同步周期，默认30s。controller中缓存了service，endpoint等信息，该配置项设置的是在没有数据变化的情况下，同步数据的周期。如果有数据变化，会第一时间更新。
* nginx-template：nginx配置文件模板路径，默认为当前目录。
* upstream-config-path：nginx upstream配置文件输出目录，输出刷新之后的upstream配置文件。
* nginx-config-path: nginx 配置文件路径，用于nginx reload动作正常运行。
* alsologtostderr：是否输出调试日志。

**2、命令启动样例**
---------------------
* 启动前migucontroller前，需要将nginx二进制可执行文件所在的目录加入到PATH中，能够被用户直接使用。
 比如：PATH=$PATH:/home/abservice/ab/tengine/sbin
* 启动命令样例如下：
`./migucontroller  --kube-apiserver-ip=172.25.43.199 --kube-apiserver-port=5443  --access-key=ICW82J1S25XHN2UJKXKM  --secret-key=5PPFWFVY2UKLYKV0GT3DQA7HVJ7N8ZCSODIR2YBP --iam-sersver-address=172.25.43.198:31943  --alsologtostderr=true --nginx-config-path=/home/abservice/ab/egsb-server/conf/nginx.conf`

**3、编译部署方式**
---------------------



**3、常见问题**
---------------------
* nginx reload失败，这个是由于nginx没有被配置到PATH中。 
failed to execute nginx -s reload -c /home/abservice/ab/egsb-server/conf/nginx.conf: sh: nginx: command not found