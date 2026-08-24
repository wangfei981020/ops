# OpsVersion Helm Chart 设计说明
从 yaml 里剥离出来的注释。**改配置前先看这里** —— 里面记的多是「改错了不会报错、只会安静出问题」的地方。

---

## `Chart.yaml`

appVersion 由 CI 用 --set 覆盖，这里只是缺省占位

---

## `values.yaml`

⚠️ 这个文件是 chart 的**默认值**，作用只有一个：保证结构齐全，模板不会 nil pointer。

真正的配置在环境文件里，每个环境一份、完整自包含：
values-local.yaml   本地（docker-desktop）
values-prod.yaml    生产（GKE infra-01 / devops）

# 为什么这里必须有完整结构

第一版把这个文件清成了只有一个哨兵键。结果是 chart **没有任何默认值** ——
环境文件里少写一个键，模板就报 `nil pointer evaluating interface {}.enabled`，
而那个报错完全不提"你少配了哪个键"。
生产第一次安装就撞上了（NOTES.txt 访问 .Values.ingress.enabled）。

⚠️ 更要紧的是这是个要卖的产品：客户会写自己的 values 文件，而且一定是残缺的。
chart 不能因为对方少写一个可选键就装不上。

# 这里的值全部是"最安全的关闭态"

入口关、指标采集关、副本数最小、镜像仓库为空 —— 单独用这个文件装出来的东西
跑不起来（拉不到镜像），这是刻意的：让"忘了传 -f"变成一次响亮的失败。

🔴 **前后端统一 tag**。同一个 chart 一次发布、一次回滚。
组件级 image.tag 留空时用这里的值；两边都填且不一致时 chart 会直接 fail
（见 _helpers.tpl 的 opsversion.requireSameTag）——
光靠人记得填同一个值是不够的：只改一处不会报错，两个镜像都能起来，
只是前端新后端旧，表现为某个接口 404 或字段缺失，最难查。

密钥由**外部单独创建**，chart 不生成 —— values 会进 git，密钥写进去等于提交到仓库。
见 deploy/secret.example.yaml：改完 kubectl apply，再装 chart。

定时采集周期（分钟）。对方 Rancher 走公网，别设太密 ——
采集对目标集群 apiserver 是有负载的，而版本本来也不会分钟级变化

---

## `values-local.yaml`

###                     本  地  （ L O C A L ）                           ###
###   docker-desktop / namespace ops-version                                ###
###   镜像仓库 registry.example.com/ops                             ###
###   入口 NodePort 30837（经 k8s-proxy，见 k8s-proxy/README.md）          ###

helm upgrade --install version ops/ops-version/deploy/helm \
-n ops-version --create-namespace \
-f ops/ops-version/deploy/helm/values-local.yaml \
--set frontend.image.tag=vX --set backend.image.tag=vX

⚠️ 本文件**完整自包含**，不依赖任何其它 values 文件继承。
与 values-prod.yaml 的键集合由 check-values-parity.mjs 比对。

⚠️ 必须为 false，见 values.yaml 里的说明

本地 Harbor。完整地址 = 这里 + 组件的 image.repository + tag

🔴 **前后端统一 tag**。同一个 chart 一次发布、一次回滚。
组件级 image.tag 留空时用这里的值；两边都填且不一致时 chart 会直接 fail
（见 _helpers.tpl 的 opsversion.requireSameTag）——
光靠人记得填同一个值是不够的：只改一处不会报错，两个镜像都能起来，
只是前端新后端旧，表现为某个接口 404 或字段缺失，最难查。

单节点集群跑 2 副本是刻意的：多副本相关的问题（会话、粘性、
滚动更新期间新旧并存）只有在多副本下才会暴露，本地就该照生产的形态验证。

⚠️ 开了 HPA 之后只在**第一次创建**时生效，之后副本数由 HPA 接管
前端无状态，多副本本身没问题。这里设 1 是跟随后端的最小形态；
要抗单点就把 replicaCount 提到 2 并把下面的 PDB 一起打开。

本地用 NodePort 固定端口，不要临时 port-forward ——
端口随机会导致每次验证的地址都不一样

⚠️ 保持改造前的数值，这次「去继承」重构不应带来任何行为变化。
与生产不同是正常的：生产按实测调过，本地只求跑得起来

本地也开自动扩容 —— 形态和生产一致才验得出问题。

⚠️ 需要 metrics-server。docker-desktop 默认没装，没装时 HPA 显示
`<unknown>/70%` 并且**永远不扩容**，而 kubectl get hpa 不会报错。
装法（kubelet 证书是自签，必须加 --kubelet-insecure-tls）：
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl -n kube-system patch deploy metrics-server --type=json \
-p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

maxReplicas 比生产小：单节点扩到 8 个副本只是把同一台机器的资源
切得更碎，验不出任何东西，还会让别的服务起不来

见上方说明：后端补上选主前不能开

🔴 单副本时必须**关掉**，不能填 minAvailable: 0。

两个原因：
1. 单副本 + minAvailable:1 会**永久阻塞节点 drain** —— 驱逐唯一那个 pod
会让可用数变 0、违反 PDB，于是节点维护和 GKE 升级一直卡着，
而报错只说 "cannot evict pod"，不会指出根因是副本数与 PDB 对不上。
2. minAvailable:0 是个「存在但不保护任何东西」的 PDB —— 将来有人把副本调上去，
会以为有它兜着，实际没有。关掉更诚实：没有保护就是没有保护。

副本数提到 2 以上时，把 enabled 改回 true。

securityContext 见 templates/_helpers.tpl 的 opsversion.securityContext

🔴 **后端目前必须单副本**，这不是性能取舍，是正确性问题。

定时采集（collector.CollectAll）**还没有选主**，每个副本都会独立跑一遍：
· 对 Kite / 对方 Rancher 的请求量翻 N 倍（采集对目标 apiserver 是有负载的）
· version_changes 是 INSERT 不是 upsert → **变更历史出现重复条目**，
「落后多少天」跟着算错
· 多副本同时启动会一起跑数据库迁移，schema_migrations 主键冲突可能让副本起不来

补上 leader election（参照 某个同类产品 的 leases 表）之后才能开 autoscaling。
在那之前把 enabled 改成 true 不会有任何报错 —— 它只是安静地把数据弄脏。

见上方说明：后端补上选主前不能开

🔴 单副本时必须**关掉**，不能填 minAvailable: 0。

两个原因：
1. 单副本 + minAvailable:1 会**永久阻塞节点 drain** —— 驱逐唯一那个 pod
会让可用数变 0、违反 PDB，于是节点维护和 GKE 升级一直卡着，
而报错只说 "cannot evict pod"，不会指出根因是副本数与 PDB 对不上。
2. minAvailable:0 是个「存在但不保护任何东西」的 PDB —— 将来有人把副本调上去，
会以为有它兜着，实际没有。关掉更诚实：没有保护就是没有保护。

副本数提到 2 以上时，把 enabled 改回 true。

securityContext 见 templates/_helpers.tpl 的 opsversion.securityContext

与 ops-data-plane 连同一个本地库（同一份种子数据）。
定时任务不会跑两遍：两边共用 leases 表选主，同名 lease 只有一个持有者
密钥由**外部单独创建**，chart 不生成 —— values 会进 git，密钥写进去等于提交到仓库。
见 deploy/secret.example.yaml：改完 kubectl apply，再装 chart。

健康检查独立端口。与业务端口分开是刻意的：业务端口被慢查询占满时，
探针打业务端口会超时 → kubelet 判定「进程没了」把 Pod 杀掉重建，
而真实问题是数据库慢，重建解决不了还会放大

定时采集周期（分钟）。对方 Rancher 走公网，别设太密 ——
采集对目标集群 apiserver 是有负载的，而版本本来也不会分钟级变化。
程序内部有下限 5 分钟，填更小的值会被抬回 5

首次启动创建的本地超管用户名；密码在 secret 的 SUPER_PASSWORD。
库里已有用户时这两个都不生效（不会覆盖已有账号）

⚠️ 本地关掉：docker-desktop 没有 ServiceMonitor/VMServiceScrape 的 CRD，
打开会让 helm install 直接失败（这是好事，比静默跳过强）

⚠️ 必须小于 istio.timeout

单节点集群上 required 会让第二个副本永远 Pending

本地不装 Istio，入口走 NodePort

---

## `values-prod.yaml`

###                  生  产  （ P R O D U C T I O N ）                    ###
###   GKE infra-01 / cluster_id=3 / namespace devops                     ###
###   镜像仓库 harbor.example.com/devops                               ###
###   入口 Istio · 只挂内网网关                                            ###

部署方式（二选一，效果相同）：

A. 直接指定（推荐，仓库里保持三个文件不动）
helm upgrade --install version ops/ops-version/deploy/helm \
-n devops -f ops/ops-version/deploy/helm/values-prod.yaml \
--set frontend.image.tag=vX --set backend.image.tag=vX

B. 改名成 values.yaml 再部署（chart 自动加载它，不用 -f）
mv values-prod.yaml values.yaml && rm values-local.yaml
helm upgrade --install version . -n ops-version \
--set frontend.image.tag=vX --set backend.image.tag=vX

⚠️ B 方案改完之后，这个目录里的 values.yaml **就是生产配置**。
别把它拷回仓库、也别在这个目录里做本地部署 ——
那会拿生产的镜像仓库、生产的 Secret 名、Istio 入口去装到别处，
而这些错误不会立刻报出来。

⚠️ 本文件**完整自包含**，不依赖任何其它 values 文件继承。
读这一个文件就能回答"生产到底是什么配置"。
与 values-local.yaml 的键集合由 check-values-parity.mjs 比对，
只在一个文件里加了键会构建失败。

⚠️ 必须为 false。chart 自带的 values.yaml 里是 true，
忘了传 -f 时模板直接 fail，而不是拿一份空配置去部署

全局：所有组件共用的东西只配一次

完整镜像地址 = imageRegistry + 组件的 image.repository + tag
harbor.example.com/devops/ops-version-backend:v0.45.0

⚠️ 前后端各写一份完整地址的话，换仓库要改两处，而**只改一处不会报错**：
改了的正常拉，没改的还在拉旧仓库 —— 两个镜像都能起来，只是版本对不上

🔴 **前后端统一 tag**。同一个 chart 一次发布、一次回滚。
组件级 image.tag 留空时用这里的值；两边都填且不一致时 chart 会直接 fail
（见 _helpers.tpl 的 opsversion.requireSameTag）——
光靠人记得填同一个值是不够的：只改一处不会报错，两个镜像都能起来，
只是前端新后端旧，表现为某个接口 404 或字段缺失，最难查。

拉私有仓需要。生产 Harbor 用现成的那个：

若改成直接拉 yourorg（imageRegistry: yourorg），要另建：
kubectl -n <ns> create secret docker-registry yourorg-pull \
--docker-server=docker.io --docker-username=yourorg --docker-password='<token>'
并把上面的 name 换成 yourorg-pull

⚠️ 开了 HPA 后这个值只在**第一次创建**时生效，之后副本数由 HPA 接管
（minReplicas=2，所以实际最少是 2）
前端无状态，多副本本身没问题。这里设 1 是跟随后端的最小形态；
要抗单点就把 replicaCount 提到 2 并把下面的 PDB 一起打开。

只写镜像名，仓库前缀来自 global.imageRegistry

⚠️ 生产**不开 NodePort**：它在每个节点上开一个端口，
等于绕过网关的鉴权，是一条容易被忘掉的公网暴露面

⚠️ request 是 HPA 算利用率的**分母**，不只是调度依据。
给太小会让阈值低到真实负载够不着；贴着实测值给又会超卖节点

⚠️ CPU limit 超过就**限流**（cfs throttling），表现为偶发的请求变慢，
而且监控上看不出是被限流的

见上方说明：后端补上选主前不能开

内存也要有阈值：静态服务几乎不吃 CPU，只看 CPU 等于没有保护。
⚠️ 内存指标基本是单向的（RSS 涨上去很难落回来），扩了多半不会自己缩

🔴 单副本时必须**关掉**，不能填 minAvailable: 0。

两个原因：
1. 单副本 + minAvailable:1 会**永久阻塞节点 drain** —— 驱逐唯一那个 pod
会让可用数变 0、违反 PDB，于是节点维护和 GKE 升级一直卡着，
而报错只说 "cannot evict pod"，不会指出根因是副本数与 PDB 对不上。
2. minAvailable:0 是个「存在但不保护任何东西」的 PDB —— 将来有人把副本调上去，
会以为有它兜着，实际没有。关掉更诚实：没有保护就是没有保护。

副本数提到 2 以上时，把 enabled 改回 true。

securityContext 见 templates/_helpers.tpl 的 opsversion.securityContext：
前后端完全一致，只有一份。uid 由 Dockerfile 单点定义（前端 101 / 后端 10001），
这里只写 runAsNonRoot、不写 runAsUser —— 两处各写一个数必然分叉

同前端：开了 HPA 后只在第一次创建时生效
🔴 **后端目前必须单副本**，这不是性能取舍，是正确性问题。

定时采集（collector.CollectAll）**还没有选主**，每个副本都会独立跑一遍：
· 对 Kite / 对方 Rancher 的请求量翻 N 倍（采集对目标 apiserver 是有负载的）
· version_changes 是 INSERT 不是 upsert → **变更历史出现重复条目**，
「落后多少天」跟着算错
· 多副本同时启动会一起跑数据库迁移，schema_migrations 主键冲突可能让副本起不来

补上 leader election（参照 某个同类产品 的 leases 表）之后才能开 autoscaling。
在那之前把 enabled 改成 true 不会有任何报错 —— 它只是安静地把数据弄脏。

后端是突发型：平时几乎不动，定时任务跑集群全量采集时冲高。
CPU 给 5 倍让采集那几分钟不被限流

见上方说明：后端补上选主前不能开

🔴 单副本时必须**关掉**，不能填 minAvailable: 0。

两个原因：
1. 单副本 + minAvailable:1 会**永久阻塞节点 drain** —— 驱逐唯一那个 pod
会让可用数变 0、违反 PDB，于是节点维护和 GKE 升级一直卡着，
而报错只说 "cannot evict pod"，不会指出根因是副本数与 PDB 对不上。
2. minAvailable:0 是个「存在但不保护任何东西」的 PDB —— 将来有人把副本调上去，
会以为有它兜着，实际没有。关掉更诚实：没有保护就是没有保护。

副本数提到 2 以上时，把 enabled 改回 true。

securityContext 见 templates/_helpers.tpl 的 opsversion.securityContext

凭据一律走 Secret，不写进 values（见文末「上线前置」）
密钥由**外部单独创建**，chart 不生成 —— values 会进 git，密钥写进去等于提交到仓库。
见 deploy/secret.example.yaml：改完 kubectl apply，再装 chart。

健康检查独立端口。与业务端口分开是刻意的：业务端口被慢查询占满时，
探针打业务端口会超时 → kubelet 判定「进程没了」把 Pod 杀掉重建，
而真实问题是数据库慢，重建解决不了还会放大

定时采集周期（分钟）。对方 Rancher 走公网，别设太密 ——
采集对目标集群 apiserver 是有负载的，而版本本来也不会分钟级变化。
程序内部有下限 5 分钟，填更小的值会被抬回 5

首次启动创建的本地超管用户名；密码在 secret 的 SUPER_PASSWORD。
库里已有用户时这两个都不生效（不会覆盖已有账号）

采集后端指标（后端 8088，与健康端点同端口）。

生产 monitoring 里 Prometheus 与 VictoriaMetrics **两套都在跑**
（kube-prometheus-stack 0.83 + victoria-metrics-operator v0.66）。
当前发 ServiceMonitor：VM operator 的 Prometheus 转换器开着，两边都能采到。

⚠️ VM 完全替代 Prometheus 后，scrapeKind 与 labels **两个都要改**，
只改一个的表现是"指标悄悄没了"。

ServiceMonitor（Prometheus Operator）或 VMServiceScrape（VictoriaMetrics Operator）

⚠️ 必须匹配采集端的选择器。不匹配时对象照样创建成功、kubectl get 一切正常、
没有任何报错，只是永远没人来抓 —— 和"根本没建"表现完全一样。
Prometheus:      kubectl get prometheus -A -o jsonpath='{.items[*].spec.serviceMonitorSelector}'
VictoriaMetrics: kubectl get vmagent -A -o jsonpath='{.items[*].spec.serviceScrapeSelector}'

⚠️ 必须小于 interval，否则 Operator 会拒绝这个对象

后端可能慢查（大集群全量拉取），比 nginx 默认 60s 给得宽。
⚠️ 必须小于 istio.timeout，否则网关先掐断，报错指向网关而非真正的慢查询

生产是多节点，用 required 强制副本分散 ——
全部落在同一节点时，那个节点一挂服务就整体不可用，PDB 也救不了

入口二选一：ingress（标准 K8s）或 istio（VirtualService），两个都开会被模板拒绝。

⚠️ 该集群**没有 nginx ingress controller**。开了 ingress 不会报错：
对象正常创建、kubectl get ingress 看着正常，只是没有任何控制器去处理它，域名就是不通

⚠️ 跨 namespace 必须写成 <ns>/<gateway名>，只写名字时 Istio 会在
**本 namespace** 找，找不到不会报错，域名就是不通。

⚠️ 只挂内网网关。旧 CMDB 额外挂了 -extra（公网），新版在公网暴露之前
要先用修好的判定重跑一遍高危清单 —— 之前两轮生产验证查出的两个 P0
都是接口把凭据发给了不该看的人

⚠️ 必须落在网关 listener 的 hosts 通配范围内，否则 Istio **静默丢弃**：
VirtualService 创建成功、kubectl get vs 一切正常、访问就是不通。
kubectl -n istio-system get gateway infra-istio-ingressgateway-inner \
-o jsonpath='{.spec.servers[*].hosts}'

TLS 不用配：inner 网关 443 上已有 *.example.com 与 *.example.com 通配证书

⚠️ 必须大于 proxyTimeoutSeconds（120）

⚠️ 刻意**不含 5xx**。

connect-failure / refused-stream 是安全的：请求根本没到应用，重发无副作用。
5xx 意味着应用收到了、处理了、然后失败 —— 对非幂等写（域名续费是真扣费）
重发就是重复扣款，而且这层重试在应用日志里和"用户手点三次"长得一样

真要开 5xx 重试必须显式声明，让它是一个有人签字的决定

── 上线前置（helm 装不上去的部分）─────────────────────────────────

1. Secret `ops-version-backend-secret`（本 chart 不创建，凭据不进 git）：
MYSQL_HOST / MYSQL_PORT / MYSQL_USER / MYSQL_PASSWORD / MYSQL_DATABASE
JWT_SECRET     —— 会话签名
CMDB_AES_KEY   —— 加密云凭据与证书私钥。⚠️ 变量名是 CMDB_AES_KEY 不是
AES_KEY，写错等于没配。⚠️ 换了它，库里已存的凭据全部解不开

⚠️ 漏配会**拒绝启动**并打印原因（config.checkSecrets）。这是刻意的：
源码里的开发默认值不会让服务报错——登录能登、页面能开、监控全绿，
只是密钥写在将要公开的 CE 源码里

2. 生产 MySQL 库先建好，排序规则统一 utf8mb4_0900_ai_ci
（不统一时跨表字符串比较抛 Error 1267，页面上只表现为"查不到数据"）。
迁移由后端启动时自动跑，不要手动改 schema_migrations —— 对不上会让 pod 起不来

3. 镜像要多架构：本地 arm64、生产 GKE amd64，单架构上去是 exec format error。
转推 Harbor 必须用 `docker buildx imagetools create`，
`docker pull && tag && push` 只会搬当前机器架构那一个

详见 docs/runbooks/ops-version-prod-deploy.md

---

## `templates/_helpers.tpl`

为什么要有 global.tag

后端地址 —— 前端 nginx 反代的目标。

这个 helper 是单 chart 最主要的收益：地址由 chart 自己推导，不需要人填。
手工同步地址是上一代反复出问题的地方（env / Secret / ConfigMap 三处，第三处最常漏），
收口在这里之后就只有一处真相。

⚠️ 曾经有个 backend.enabled=false 分支，用来反代到 chart 之外的既有后端
（迁移期指向 enterprise/ops-data-plane）。后端搬进本仓库后那条路径就没有用了，
留着反而危险：那是**另一个产品**的后端，接上去之后新前端要的字段它没有，
而缺字段在界面上只表现为"这块永远是空的" —— 不报错、不 404、监控看不见。
已删除，后端一律由本 chart 部署。

反亲和：把同一组件的副本尽量分散到不同节点

镜像地址：global.imageRegistry + 组件的 repository。

# 为什么要有全局前缀

前后端各写一份完整地址的话，换仓库要改两处 —— 而**只改一处不会报错**：
改了的那个正常拉，没改的那个还在拉旧仓库。两个镜像都能起来，
只是版本对不上，表现为"前端升了后端没升"这类最难复现的错配。

registry 为空时用组件自己的 repository 原样（兼容写全路径的老配置）。

统一 tag。优先级：组件级 image.tag > global.tag > Chart.AppVersion。

# 为什么要有 global.tag

前后端**必须是同一个版本**：同一个 chart 一次发布、一次回滚。
让人在两个地方各填一遍，迟早只改一处 —— 而只改一处**不会报错**：
两个镜像都拉得到、都能起来，只是前端是新的后端是旧的，
表现为某个接口 404 或字段缺失，排查时谁都想不到是版本错配。

⚠️ 从 某个同类产品 复制 chart 时踩过：那份 values 里写着 global.tag，
但 helper 根本不读它 —— 设了不生效，且不报错。这里把它做成真的。

前后端 tag 一致性闸。在任何模板渲染镜像前调用。

光靠约定和注释保证不了「一个 tag」—— 必须让不一致**装不上**。

镜像地址：global.imageRegistry + 组件的 repository + 统一 tag。

registry 为空时用组件自己的 repository 原样（兼容写全路径的老配置）。

安全上下文。前后端**完全一致**，所以只有一份。

⚠️ 不要为了"以后可能不一样"提前拆成两份：
两份一模一样的配置，改的时候必然只改一处，而分叉之后两边都能跑，
没人会发现 —— 直到某个按属主授权的地方失败，报错却只说 permission denied。
真出现了组件间差异，再在那时拆，并写清楚为什么不同。

没传环境 values 文件时直接失败。
拿一份空配置渲染出来的 release 是能装的 —— 镜像地址、入口、凭据全是空，
而失败点会推迟到 pod 起不来，那时排查的人看到的是 ImagePullBackOff，
不会想到根因是「helm 命令少了一个 -f」。

---

## `templates/_hpa.tpl`

缩容给长稳定窗口：流量是尖峰型的，缩太快会在下一个尖峰立刻又扩，来回抖动。

⚠️ 内存指标基本上是**单向**的：Go 运行时和 nginx 都不怎么把内存还给 OS，
RSS 涨上去很难落回来。所以内存触发的扩容多半不会自己缩回去 ——
这不是配置错了，是内存这个指标的性质。真要缩回去通常得靠重启。
把它当"涨上去就别再涨"的保护，不要指望它像 CPU 那样自动收敛。

HPA 片段，前后端共用。

⚠️ 抽成一份是刻意的：前后端各写一份的话，改扩容策略时必然只改一处，
而"另一处没跟上"在集群里完全看不出来 —— 两个 HPA 都存在、都健康，
只是行为不一样，要到某次扩容没按预期发生才会有人发现。

参数：ctx=根上下文  name=目标 Deployment 名  cfg=autoscaling 配置  component=标签用

⚠️ 调用处必须写 `{{ include ... }}`，**不能写 `{{- include ... }}`**。
带 `-` 会把前一个文档末尾的换行吃掉，本模板开头的 `---` 于是粘到上一行尾部：

    app.kubernetes.io/component: backend---
    apiVersion: autoscaling/v2

两个对象变成一个非法文档。后果极其隐蔽：`helm template | grep -c "kind:"`
数出来还是对的（两行 kind 都在），helm 也不报错，
但 apply 时前一个对象（PDB）根本不会被创建 —— 而 `helm get manifest`
里它明明在。真的靠 kubectl get 才发现集群里一个 PDB 都没有。

CPU 与内存**任一**超阈值就扩容（HPA 取各指标算出的副本数的最大值），
      而缩容要**全部**低于阈值才会发生。这是 HPA 的既定语义，不是配置项。

---

## `templates/backend.yaml`

⚠️ 开了 HPA 就**不能**再写 replicas。

两者都在时，每次 helm upgrade 都会把副本数按回 replicaCount，
把 HPA 扩出来的副本砍掉；HPA 随后再扩回去。
表现是"每次发布之后容量掉一截然后慢慢恢复"，
而发布日志、HPA 事件、Deployment 状态全都正常 —— 没有任何一处会报错。

只留 3 份历史。默认 10 —— 每次发布留一个 ReplicaSet，
频繁发布会在 etcd 里堆一堆没人看的对象（DEV 集群 1.86 万对象过半是这类垃圾）。
3 份足够回滚：真要回到更早的版本，helm rollback 看的是 release 历史不是 ReplicaSet。

Pod 级安全上下文：容器级那份的**兜底**。

两处都写不是冗余：容器级 securityContext 是 values 里可被覆盖的，
有人为了排障临时改一版就可能把 runAsNonRoot 丢掉，
而 Pod 级这份留在模板里、覆盖不到，kubelet 仍会拒绝以 root 启动的镜像。
也是 Pod Security Standards "restricted" 要求的形态。

⚠️ 多副本下这三件事必须成立，否则副本越多问题越多：
1. 定时采集任务要选主（leases 表已就绪），不然 N 个副本重复采集
2. 数据库迁移要加锁，多副本同时启动会抢跑
3. license 状态要定期从 DB 热加载，不能只在启动时读，
否则激活后要重启全部副本才生效
详见 docs/plans/opsplane-rewrite-plan.md §6
⚠️ 健康端点在 **metrics 端口（8088）**，不是业务端口。
这是后端有意的设计：业务端口 hang 死时探活仍然可用，
不至于把"卡住"表现成"进程没了"。探针打错端口的表现是
一直 404 → CrashLoop，而应用其实完全正常。

PDB 的判据必须看**实际会有几个副本**。

⚠️ 原来只看 replicaCount，而开了 HPA 之后那个值只在第一次创建时生效，
之后副本数由 HPA 的 minReplicas 决定。生产把 replicaCount 设成 1、
minReplicas 设成 2 时，实际跑 2 个副本却**一个 PDB 都不生成** ——
节点排水时两个副本可以被同时驱逐，而 helm 一切正常、kubectl get pdb 是空的，
没有任何地方会提示你少了这层保护。

---

## `templates/frontend.yaml`

只留 3 份历史。默认是 10 —— 每次发布留一个 ReplicaSet，
频繁发布的服务会在 etcd 里堆一堆没人看的对象（DEV 集群 1.86 万对象里过半是这类垃圾）。
3 份足够回滚：真要回到更早的版本，helm rollback 看的是 release 历史不是 ReplicaSet。

先起新的再停旧的，滚动期间容量不下降。
静态前端很轻，多一个副本的开销可忽略，不值得为省这点资源冒中断风险。

nginx.conf 变了要触发滚动 —— ConfigMap 更新本身不会重启 Pod，
少了这个校验和，改完配置以为生效了其实没有。

优雅退出：nginx 收到 SIGTERM 立刻断连接，
先睡 5 秒让 Endpoints 摘除生效，避免滚动更新时零星 502。

Pod 级安全上下文：容器级那份的**兜底**。

两处都写不是冗余：容器级 securityContext 是 values 里可被覆盖的，
有人为了排障临时改一版就可能把 runAsNonRoot 丢掉，
而 Pod 级这份留在模板里、覆盖不到，kubelet 仍会拒绝以 root 启动的镜像。
也是 Pod Security Standards "restricted" 要求的形态。

三种探针职责不同，参数不能共用一套：
startup   保护慢启动的容器不被 liveness 杀掉
readiness 决定是否接流量，失败就摘出 Endpoints
liveness  只在真死了才重启，阈值必须比 readiness 宽松

readOnlyRootFilesystem 下 nginx 需要的可写目录必须显式挂出来

⚠️ 只在副本数 > 1 时创建。
单副本 + minAvailable:1 会让节点排空永远卡住 —— 驱逐这唯一的 Pod 违反 PDB，
不驱逐节点就下不掉。这是集群升级时最常见的卡死。

PDB 的判据必须看**实际会有几个副本**。

⚠️ 原来只看 replicaCount，而开了 HPA 之后那个值只在第一次创建时生效，
之后副本数由 HPA 的 minReplicas 决定。生产把 replicaCount 设成 1、
minReplicas 设成 2 时，实际跑 2 个副本却**一个 PDB 都不生成** ——
节点排水时两个副本可以被同时驱逐，而 helm 一切正常、kubectl get pdb 是空的，
没有任何地方会提示你少了这层保护。

---

## `templates/frontend-configmap.yaml`

nginx-unprivileged：非 root、监听 8080

readOnlyRootFilesystem 下所有可写路径都要落到 emptyDir 挂的 /tmp，
否则 nginx 启动时写临时目录失败，直接 CrashLoop。

探针专用端点。不要拿 / 做探针：它会返回整个 index.html，
探针流量白占带宽，日志也被刷满。

带内容哈希的产物可以长缓存，改动必然换文件名

⚠️ index.html 与 version.json 绝不能缓存。
缓存了会出这种事故：发新版后用户手里是旧 index.html，
它引用的 assets 哈希文件已经不存在了 → 整页白屏，
且只发生在老用户身上，新访客一切正常，极难复现。

后端地址由 chart 推导（见 _helpers.tpl 的 opsversion.backendAddr），
不是人填的。改部署形态时这里自动跟着变。

⚠️ $http_host 而不是 $host：$host **不含端口**。
后端要用它拼 OIDC 的 redirect_uri，端口丢了就拼成
http://localhost/api/auth/sso/callback（少了 :30833），
身份源那边只会回一句 redirect_uri_mismatch ——
那句话既不说期望值也不说实际值，纯靠猜。
任何"后端需要知道自己对外是什么地址"的功能都栽在这一行上。

SSE / 流式响应不能被缓冲，否则告警推送要等缓冲区满才到前端

SPA 兜底：前端路由的深链直接访问时也要返回 index.html

---

## `templates/ingress.yaml`

生产形态：Service 走 ClusterIP，由 Ingress + 域名对外暴露。
/api 不单独配路由 —— 它由前端 nginx 反代到后端，
在 Ingress 上再切一刀会让"前端能通、接口 404"这类问题多一个排查层。

---

## `templates/virtualservice.yaml`

为什么和 ingress.yaml 并存而不是二选一替换

同 chart 内生成，不需要人填 —— 地址靠手工同步是上一代反复出问题的根源

⚠️ 必须大于前端 nginx 的 proxyTimeoutSeconds。
反过来的话网关先掐断，用户看到的是网关的 504，
而真正的原因（某个慢查询）在后端日志里，两边对不上。

Istio 形态的入口。Service 走 ClusterIP，由 VirtualService 挂到 ingressgateway 上。

# 为什么和 ingress.yaml 并存而不是二选一替换

这是个要卖的产品：多数客户用标准 K8s Ingress，装了 Istio 的用这个。
删掉任何一个都会让一部分客户装不上。

# ⚠️ 两个都开会打架

同一个域名有两条入口时，实际生效的那条取决于集群里哪个控制器先接管，
而两边的超时/重试/请求体上限通常是不一样的 —— 症状是"偶发的行为不一致"，
最难查的那种。所以这里直接拒绝，不留给运行期。

# /api 不单独配路由

由前端 nginx 反代到后端。在网关上再切一刀，会让"前端能通、接口 404"
这类问题多一个排查层，而这一层的配置错误没有任何直接症状。

⚠️ 重试条件的安全闸。

connect-failure / refused-stream 是安全的：请求**根本没到应用**，重发不会产生第二次副作用。
5xx 不是：它意味着应用收到了、处理了、然后返回 500。

本产品里有**不可回退的对外动作**：手动触发 Harbor replication（M2）会真的把镜像
推到客户公司的 Harbor。如果 POST 在**已经触发之后**才出错返回 500，
网关重试会再触发两次 —— 对方那边平白多出两次同步执行，
而这一层重试在应用日志里长得和"用户手点了三次"一模一样，事后根本分不出来。

采集类接口是幂等的（全量覆盖），重试无害；但网关的 retryOn 是**全局**的，
分不出哪个接口幂等。所以只能按最危险的那个来定。

真要开的话必须显式声明 allowUnsafeRetryOn —— 让它成为一个有人签字的决定。

---

## `templates/metrics-scrape.yaml`

为什么两种都要，而不是只发 ServiceMonitor

让采集端抓后端指标。支持两种采集端，用 metrics.scrapeKind 选：

  ServiceMonitor   Prometheus Operator（kube-prometheus-stack）
  VMServiceScrape  VictoriaMetrics Operator 的原生 CRD

# 为什么两种都要，而不是只发 ServiceMonitor

VM operator 默认会把 ServiceMonitor 自动转成 VMServiceScrape
（VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE，默认 true），所以只发
ServiceMonitor 在 VM 环境下**通常**也能工作。

但那是一层隐式依赖：转换器被关掉时（有些团队为了避免和 Prometheus 双采而关），
ServiceMonitor 照样创建成功、没有任何报错，只是没人来抓。
把 VMServiceScrape 做成一等选项，是为了让"用哪个采集端"成为一个
**显式写在 values 里的决定**，而不是依赖对方的默认配置。

⚠️ 两种只能选一个。都发的话，Prometheus 和 vmagent 会同时抓同一个端点，
指标进两套库、告警可能重复触发，而两边看着都正常。

---

## `templates/NOTES.txt`

Prometheus:      kubectl get prometheus -A -o jsonpath='{.items[*].spec.serviceMonitorSelector}'
VictoriaMetrics: kubectl get vmagent -A -o jsonpath='{.items[*].spec.serviceScrapeSelector}'
上面输出的标签要能匹配下面这些

最终以采集端的 Targets 页里能搜到、并且是 UP 为准

判据要看**实际会有几个副本**，不能只看 replicaCount。
开了 HPA 时那个值只在第一次创建时生效，之后由 minReplicas 决定。

⚠️ 之前只看 replicaCount，于是生产（replicaCount=1 + minReplicas=2）
每次部署都打这条"当前是单副本"——而实际跑着 2 个。
一条恒假的警告会让人对所有警告脱敏，比不打更糟。

---

## `secret.example.yaml`

OpsVersion 后端密钥 —— **单独 apply，不由 chart 生成**

用法：
1. 复制一份改成真实值（别改文件名之外的 metadata.name，chart 靠它引用）
2. kubectl apply -f secret.yaml -n <namespace>
3. 再 helm upgrade --install

⚠️ 这份文件是**示例**，真实密钥不要提交进 git。
生产建议用 kubectl create secret 直接建，不落文件。

固定名字。chart 的 values 里 backend.envFrom 引用的就是它，两边靠约定对齐

数据库。库要先建好，表由程序启动时自动迁移
CREATE DATABASE ops_version DEFAULT CHARSET utf8mb4;
⚠️ go-sql-driver 格式，密码里不要有 @ # : / 等字符，否则 DSN 解析出错

🔴 加密组织凭据用（Kite / Rancher 的密码与 token 都用它加密后入库）。
至少 16 字符，随机生成：openssl rand -hex 24
⚠️ 一旦有数据后**不能再改**：改了之后已存的凭据全部解不开，
表现为所有组织采集报「认证失败」，而密码其实一个都没错。

会话签名。改了会让所有人被登出，但不影响数据

首次启动创建的本地超管密码。
🔴 本地账号在接了 SSO 之后也要保留 —— SSO 挂了时它是唯一的逃生通道。
库里已有用户时这个值不生效（不会覆盖已有密码）
