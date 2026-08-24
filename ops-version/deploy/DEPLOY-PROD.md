# OpsVersion 生产部署

镜像：`yourorg/ops-version-backend:v0.2.0` / `yourorg/ops-version-frontend:v0.2.0`
（前后端同一个 tag，amd64，Trivy 全严重度 0 漏洞）

> 所有环境变量**只在 Secret 一个地方**，values 里没有 env。

---

## 前置

| 项 | 说明 |
|---|---|
| 命名空间 | 下文用 `devops`，按实际改 |
| 数据库 | MySQL 8，**库要先手工建**，表由程序启动时自动迁移 |
| 镜像拉取 | 生产 Harbor 用现成的 `ops-harbor-login-secret`；直连 yourorg 要另建 |
| 入口 | Istio VirtualService，挂内网网关 |
| 监控 | ServiceMonitor（或 VMServiceScrape） |

---

## 步骤 1 · 建库和账号

程序**不会自己建库**（建库要 root 权限，而应用账号不该有）。库不存在时启动会直接报
`database "ops_version" does not exist; create it first` 然后退出 —— 这是刻意的，
比自动建库更安全：拼错库名时会立刻发现，而不是悄悄建出一个空库来。

```sql
CREATE DATABASE ops_version DEFAULT CHARSET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 独立账号，只给这一个库的权限
CREATE USER 'ops_version_user'@'%' IDENTIFIED BY '换成真实密码';
GRANT ALL PRIVILEGES ON ops_version.* TO 'ops_version_user'@'%';
FLUSH PRIVILEGES;
```

> 密码里**可以用任意字符**（`@ # : /` 都行）—— 连接串由程序用 `mysql.Config` 构造，
> 转义交给驱动，不是字符串拼接。

验证账号能连：

```bash
kubectl -n devops run mysql-check --rm -it --restart=Never --image=mysql:8 -- \
  mysql -h<mysql地址> -uops_version_user -p'密码' -e "SELECT 1" ops_version
```

---

## 步骤 2 · 建 Secret（所有环境变量都在这）

```bash
cd ops/ops-version/deploy
cp secret.example.yaml secret.yaml

# ① 这条直接执行，自动填两个随机密钥（不用手改）
sed -i.bak -e "s|__GEN_ENCRYPT_KEY__|$(openssl rand -hex 24)|" \
           -e "s|__GEN_JWT_SECRET__|$(openssl rand -hex 24)|" \
           secret.yaml && rm -f secret.yaml.bak

# ② 手填剩下 4 处 __FILL__
vim secret.yaml

# ③ apply 完就删，别留在磁盘上、别提交进 git
kubectl apply -f secret.yaml -n devops
rm secret.yaml
```

### 全部 12 个变量（secret.yaml 里没有注释，说明都在这张表）

| 变量 | 默认 | 必填 | 说明 |
|---|---|---|---|
| `MYSQL_HOST` | — | ✅ **手填** | 如 `mysql.db-services` |
| `MYSQL_PORT` | `3306` | | |
| `MYSQL_USER` | — | ✅ **手填** | 步骤 1 建的账号，如 `ops_version_user` |
| `MYSQL_PASSWORD` | — | ✅ **手填** | **任意字符都行**（`@ # : /` 都可以）—— 连接串由程序用 `mysql.Config` 构造，转义交给驱动 |
| `MYSQL_DATABASE` | `ops_version` | | 库要先建好，表自动迁移 |
| `ENCRYPT_KEY` | — | ✅ sed 自动填 | ≥16 字符。加密 Kite/Rancher 凭据。🔴 **有数据后不能再改** —— 改了已存凭据全部解不开，表现为所有组织报「认证失败」而密码其实都对 |
| `JWT_SECRET` | — | ✅ sed 自动填 | ≥16 字符。改了只是所有人被登出，不影响数据 |
| `SUPER_USER` | `admin` | | 初始超管用户名 |
| `SUPER_PASSWORD` | — | ✅ **手填** | 初始超管密码。库里**已有用户时不生效**，不会覆盖。🔴 接了 SSO 之后也要保留本地账号 —— SSO 挂了时它是唯一逃生通道 |
| `PORT` | `8080` | | 业务端口 |
| `METRICS_PORT` | `8088` | | 健康检查 + `/metrics`。与业务端口分开是刻意的：业务端口被慢查询占满时，探针打业务端口会超时 → kubelet 判定「进程没了」把 Pod 杀掉重建，而真实问题是数据库慢，重建解决不了还会放大 |
| `LOG_LEVEL` | `info` | | `debug` / `info` / `warn` / `error`。**接新数据源时先开 debug**：能看到每次 Kite/Rancher 请求的 URL、状态码、耗时，每个 ns 的匹配判定，以及多少 workload 归并成多少服务。跑顺了改回 `info`。改这个**不需要重新构建镜像**，改 Secret 后 `rollout restart` 即可。取值拼错会打一条 warn 说明回落到 info，不会静默 |
| `COLLECT_INTERVAL_MIN` | `30` | | 采集周期（分钟）。**程序内有下限 5**，填更小会被抬回 5 —— 对方 Rancher 走公网，太密是给对方制造负载，而版本本来也不会分钟级变化 |

缺 `MYSQL_HOST` / `MYSQL_USER` / `MYSQL_PASSWORD` / `ENCRYPT_KEY` / `JWT_SECRET`
任意一个，后端会**拒绝启动**并打印缺了哪个，不会带着坏配置跑。

⚠️ 改完 Secret 要重启后端才生效（envFrom 注入，k8s 不会自动重启 Pod）：
```bash
kubectl -n devops rollout restart deploy/version-ops-version-backend
```

---

## 步骤 3 · 确认镜像拉取密钥

`values-prod.yaml` 默认用 `ops-harbor-login-secret`（生产 Harbor 现成的）：

```bash
kubectl -n devops get secret ops-harbor-login-secret
```

若要**直连 yourorg**（镜像已推那儿），另建一个并改 registry：

```bash
kubectl -n devops create secret docker-registry yourorg-pull \
  --docker-server=docker.io --docker-username=yourorg --docker-password='<token>'
```

装的时候加：`--set global.imageRegistry=yourorg --set 'global.imagePullSecrets[0].name=yourorg-pull'`

否则先把两个镜像从 yourorg 同步进生产 Harbor，values 不用改。

---

## 步骤 4 · 确认网关和域名

```bash
# 网关存在吗
kubectl -n istio-system get gateway infra-istio-ingressgateway-inner

# 🔴 域名必须落在网关 listener 的 hosts 通配范围内，否则 Istio **静默丢弃**：
#    VirtualService 创建成功、kubectl get vs 一切正常、访问就是不通、没有任何报错
kubectl -n istio-system get gateway infra-istio-ingressgateway-inner \
  -o jsonpath='{.spec.servers[*].hosts}'
```

---

## 步骤 5 · 确认监控选择器

```bash
# Prometheus Operator：
kubectl get prometheus -A -o jsonpath='{.items[*].spec.serviceMonitorSelector}'
# VictoriaMetrics：
kubectl get vmagent -A -o jsonpath='{.items[*].spec.serviceScrapeSelector}'
```

把结果填进 `values-prod.yaml` 的 `metrics.labels`（默认 `release: kube-prometheus-stack`），
用 VM 的话把 `metrics.scrapeKind` 改成 `VMServiceScrape`。

> ⚠️ label 不匹配时对象照样创建成功、`kubectl get servicemonitor` 一切正常，
> **只是永远没人来抓** —— 和根本没建表现完全一样。

---

## 步骤 6 · 安装

```bash
helm upgrade --install version ops/ops-version/deploy/helm \
  -n devops \
  -f ops/ops-version/deploy/helm/values-prod.yaml \
  --set global.tag=v0.2.0 \
  --set 'istio.hosts[0]=opsversion.example.com'
```

`global.tag` 一处设置，前后端都跟着 —— 两边 tag 不一致时 chart 会直接 fail，装不上。

产出：2 Deployment（各 1 副本）+ 2 Service（ClusterIP）+ ConfigMap + VirtualService + ServiceMonitor。
**没有 PDB 和 HPA**，见下方「为什么单副本」。

---

## 步骤 7 · 验证

```bash
kubectl -n devops get pod -l app.kubernetes.io/name=ops-version
kubectl -n devops logs -l app.kubernetes.io/component=backend --tail=50

# 迁移是否跑完（应有 12 张表）
kubectl -n devops exec deploy/version-ops-version-backend -- true   # 确认 Pod 在
# 或直接连库：SHOW TABLES;  → audit_logs / orgs / service_versions / …

# 健康检查
kubectl -n devops port-forward deploy/version-ops-version-backend 8088:8088
curl localhost:8088/health   # → ok
curl localhost:8088/ready    # → ready（这个会查库）
curl localhost:8088/metrics | grep opsversion_
```

浏览器打开 `https://opsversion.example.com`，用 `SUPER_USER` / `SUPER_PASSWORD` 登录。

---

## 步骤 8 · 配组织

这一版前端还没有组织编辑表单，先用 API（登录后带 Cookie）：

```bash
# 我方 Kite —— 一个 endpoint 打多个集群
curl -X POST https://opsversion.example.com/api/orgs -b cookie.txt \
  -H 'Content-Type: application/json' -d '{
  "name":"我方","product":"平台A","provider_type":"kite","auth_type":"password",
  "endpoint":"https://kite.example.com","username":"<账号>","password":"<密码>",
  "harbor_host":"harbor.example.com","harbor_project":"appA","is_self":true,
  "envs":[
    {"env":"UAT","cluster_refs":["uat-cluster-01"],"ns_include":["app-*"],
     "ns_exclude":["app-uat"],"compare_enabled":true},
    {"env":"PROD","cluster_refs":["app-prod-cluster"],"ns_include":["app-*"],
     "compare_enabled":true}]}'
```

不确定 cluster 怎么填时，先让它列出来：

```bash
curl https://opsversion.example.com/api/orgs/1/clusters?env=UAT -b cookie.txt
```

Rancher 组织把 `provider_type` 换成 `rancher`、`cluster_refs` 填 clusterId（`c-m-xxxx`）。
**一个公司有两套 Rancher（UAT 一套 PROD 一套）时**，在 `envs` 里各自填 `endpoint` + 凭据，
留空则继承组织级。

配完点「测连通」，它会明确区分 **认证失败 / 网络不可达 / 权限不足**，而不是笼统一句「连接失败」。

---

## 步骤 9 · 配告警（建议）

```yaml
# 数据陈旧（含「从未成功过」）—— 对账结论不完整的根因
- alert: OpsVersionStaleData
  expr: time() - opsversion_last_success_timestamp_seconds > 3600
  for: 10m

# 持续失败
- alert: OpsVersionCollectFailing
  expr: increase(opsversion_collect_total{status!="success"}[15m]) > 3
  for: 5m

# 采集成功但服务数为 0 → ns 规则被改坏了，不是对方下线了服务
- alert: OpsVersionEmptyResult
  expr: opsversion_services_total == 0 and opsversion_last_success_timestamp_seconds > 0
  for: 15m
```

---

## 为什么是单副本

**不是性能取舍，是正确性问题。** 后端的定时采集**还没有选主**，多副本会各跑一遍：

- 对 Kite / 对方 Rancher 的请求量翻 N 倍
- `version_changes` 是 INSERT 不是 upsert → **变更历史插入重复记录**，「落后多少天」跟着算错
- 多副本同时启动一起跑迁移，`schema_migrations` 主键冲突可能让副本起不来

而且**全程不报错**，只是安静地把数据弄脏。补上 leader election 之前，
`backend.autoscaling.enabled` 必须保持 `false`。

PDB 同样关掉：单副本配 `minAvailable: 1` 会**永久阻塞节点 drain**（驱逐唯一的 pod
会让可用数变 0），节点维护和 GKE 升级都会卡住，而报错只说 "cannot evict pod"。
副本数提到 2 以上时再把 PDB 打开。

---

## 回滚

```bash
helm -n devops history version
helm -n devops rollback version <REVISION>
```

前后端同一个 chart、同一个 tag，一次回滚两个都退回去。

数据库迁移**不会回滚** —— 新版加的表/列会留着。这是刻意的：
自动回滚 schema 比不回滚危险得多（可能丢数据）。真要退 schema 得手工来。
