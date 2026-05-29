# certupload

<p align="center">
  <strong>Kubernetes Operator for Automatic Certificate Synchronization to Alibaba Cloud SSL Services</strong>
</p>

<p align="center">
  <a href="#简介">简介</a> •
  <a href="#快速开始">快速开始</a> •
  <a href="#开发指南">开发指南</a> •
  <a href="#发布部署">发布部署</a> •
  <a href="#故障排查">故障排查</a>
</p>

---

## 简介

`certupload` 是一个基于 Kubebuilder 框架构建的 Kubernetes Operator，用于自动化管理 TLS 证书的生命周期。它能够将 cert-manager 生成的证书自动同步到阿里云 SSL 证书服务（CAS），并灵活配置到 OSS 存储桶域名或 CDN 加速域名，实现证书的自动续期和部署。

### 核心功能

- 🔄 **自动同步** - 监听 cert-manager 颁发的证书，自动上传至阿里云 SSL 证书服务（CAS）
- 🚀 **灵活部署** - 按需配置同步目标：配置了 OSS 才更新 OSS，配置了 CDN 才更新 CDN，互不干扰
- ☁️ **CDN 支持** - 自动将 CAS 证书绑定到阿里云 CDN 加速域名，一站式 HTTPS 配置
- ♻️ **证书续期** - 支持 cert-manager 证书自动续期，无缝更新云端证书
- 📊 **状态监控** - OSS/CDN 独立同步状态，便于定位问题
- 🔗 **跨命名空间** - 支持引用不同命名空间中的 Secret 资源

### 工作原理

```
┌──────────────┐      ┌──────────────┐      ┌─────────────────────┐
│  cert-manager │─────▶│  CertUpload  │─────▶│ Alibaba Cloud       │
│   (颁发证书)   │      │   Operator   │      │  ├─ CAS (证书存储)   │
└──────────────┘      └──────────────┘      │  ├─ OSS (Bucket域名) │
                             │               │  └─ CDN (加速域名)   │
                             │               └─────────────────────┘
                             ▼
                      ┌──────────────┐
                      │  Status &    │
                      │   Events     │
                      └──────────────┘
```

### CRD 配置说明

CertUpload 支持四种灵活的配置模式，按需选择更新目标：

#### 模式一：仅 OSS

```yaml
spec:
  region: "cn-hangzhou"
  # 配置了 OSS，才会更新 OSS Bucket 域名证书
  oss:
    bucket: "my-bucket"
    domain: "oss.example.com"
  certManagerCertRef:
    name: example-com-tls
```

#### 模式二：仅 CDN

```yaml
spec:
  region: "cn-hangzhou"
  # 配置了 CDN，才会更新 CDN 加速域名证书
  cdn:
    domain: "cdn.example.com"
  certManagerCertRef:
    name: example-com-tls
```

#### 模式三：OSS + CDN 同时

```yaml
spec:
  region: "cn-hangzhou"
  # 同时配置 OSS 和 CDN，两个目标都会更新
  oss:
    bucket: "my-bucket"
    domain: "oss.example.com"
  cdn:
    domain: "cdn.example.com"
  certManagerCertRef:
    name: example-com-tls
```

#### 模式四：仅上传 CAS（uploadOnly）

```yaml
spec:
  region: "cn-hangzhou"
  # 显式声明只上传到 CAS，不绑定 OSS 或 CDN
  uploadOnly: true
  # CAS 证书名自动取自 cert-manager Certificate 的 DNS 名称
  certManagerCertRef:
    name: example-com-tls
```

#### Status 字段说明

| 字段 | 说明 |
|------|------|
| `status.casCertificateId` | CAS 中存储的证书 ID |
| `status.ossStatus` | OSS 同步结果：`Succeeded` / `Failed` / `Skipped` |
| `status.ossLastSyncTime` | OSS 最后同步时间 |
| `status.ossErrorMessage` | OSS 同步错误信息 |
| `status.cdnStatus` | CDN 同步结果：`Succeeded` / `Failed` / `Skipped` |
| `status.cdnLastSyncTime` | CDN 最后同步时间 |
| `status.cdnErrorMessage` | CDN 同步错误信息 |

> **使用原则**：配了才干活，不配不动手。`oss`、`cdn`、`uploadOnly` 按需选择，各自独立。

## 快速开始

用 [Kind](https://kind.sigs.k8s.io/) 在本地快速体验。

### 前置要求

| 工具 | 版本要求 |
|------|---------|
| Go | v1.24.6+ |
| Docker | 17.03+ |
| Kind | 最新版 |
| kubectl | v1.11.3+ |

### 1. 创建 Kind 集群

```bash
kind create cluster --name certupload-test
```

### 2. 构建镜像并加载

```bash
export IMG=certupload:test
make docker-build IMG=$IMG
kind load docker-image certupload:test --name certupload-test
```

### 3. 部署控制器

```bash
cd config/manager && kustomize edit set image controller=certupload:test && cd ../..
kustomize build config/default | kubectl apply -f -

# 等待就绪
kubectl wait --for=condition=ready pod -l control-plane=controller-manager \
  -n certupload --timeout=120s
```

### 4. 安装 cert-manager（必需）

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.0/cert-manager.yaml
kubectl wait --for=condition=ready pod -l app=cert-manager \
  -n cert-manager --timeout=120s
```

### 5. 创建测试资源

```bash
# 创建阿里云凭证 Secret（替换为真实凭证）
kubectl apply -f config/samples/secret-sample.yaml

# 创建 CertUpload 资源
kubectl apply -f config/samples/certupload-sample.yaml
```

### 6. 验证结果

```bash
# 查看资源状态
kubectl get certupload -A

# 查看详情
kubectl describe certupload certupload-oss-sample

# 查看控制器日志
kubectl logs -n certupload deployment/certupload-controller-manager -c manager -f
```

### 7. 清理

```bash
kind delete cluster --name certupload-test
```


## 开发指南

### 项目结构

```
certupload/
├── api/v1/                # CRD 类型定义
├── cmd/main.go              # 控制器入口
├── config/
│   ├── crd/bases/           # 生成的 CRD YAML
│   ├── rbac/                # 生成的 RBAC
│   ├── samples/             # 示例 CR
│   └── default/             # Kustomize 配置
├── internal/
│   ├── controller/          # 调和逻辑 + 测试
│   └── aliyun/              # 阿里云 SDK 封装
├── dist/install.yaml        # 发布安装包
├── test/e2e/                # 端到端测试
├── Dockerfile
├── Makefile
└── PROJECT
```

### 开发工作流

```bash
# 1. 修改 CRD 类型（api/v1/certupload_types.go）
# 2. 重新生成代码
make manifests generate

# 3. 修改控制器逻辑（internal/controller/）
# 4. 本地运行（使用当前 kubeconfig 上下文）
make run

# 5. 运行测试
make test

# 6. 代码检查（自动修复）
make lint-fix
```

### 本地调试

不部署到集群，直接在本机启动控制器，方便断点调试和快速迭代。

**前置条件：**
- 已有可用的 K8s 集群（Kind 或远程均可）
- `kubectl` 当前上下文指向目标集群
- CRD 已安装到集群

**步骤：**

```bash
# 1. 安装 CRD（不部署控制器）
make install

# 2. 本地启动控制器（前台运行，Ctrl+C 停止）
make run
```

控制台会打印控制器日志，直接看到调和过程和错误信息。

**IDE 断点调试（GoLand / VSCode）：**

```bash
# GoLand：直接运行 cmd/main.go，或创建 Go Build 配置
#   Run kind: Package
#   Package path: wyundong.com/certupload/cmd
#   Working directory: $PROJECT_DIR$

# VSCode：在 .vscode/launch.json 中添加
{
  "name": "Launch Controller",
  "type": "go",
  "request": "launch",
  "mode": "auto",
  "program": "${workspaceFolder}/cmd/main.go",
  "env": {},
  "args": []
}
```

**注意：** 本地运行时控制器使用当前 `~/.kube/config` 的上下文，确保已指向正确的集群。

### 常用命令

| 命令 | 说明 |
|------|------|
| `make manifests` | 生成 CRD YAML + RBAC |
| `make generate` | 生成 DeepCopy 方法 |
| `make run` | 本地运行控制器 |
| `make test` | 单元测试（含 envtest） |
| `make test-e2e` | 端到端测试（需 Kind） |
| `make lint` | 代码检查 |
| `make lint-fix` | 代码检查 + 自动修复 |
| `make fmt` | 格式化代码 |
| `make vet` | 静态分析 |
| `make build` | 编译二进制 |

## 发布部署

面向生产环境的部署方式。

### 前置：构建并推送镜像

```bash
export IMG=<your-registry>/certupload:<tag>

# 本机构建
make docker-build docker-push IMG=$IMG

# 交叉编译（如 ARM64 机器为 AMD64 服务器构建）
make docker-buildx IMG=$IMG PLATFORMS=linux/amd64
```

> **注意：** 确保目标集群能拉取镜像。如使用私有仓库，需配置 imagePullSecrets。

### 方式一：YAML 安装包（推荐）

生成单一 `dist/install.yaml`，用户只需一条 `kubectl apply` 即可完成安装。

```bash
# 生成安装包
make build-installer IMG=$IMG

# 安装
kubectl apply -f dist/install.yaml

# 卸载
kubectl delete -f dist/install.yaml
```

发布到 GitHub 后，用户可直接从 URL 安装：

```bash
kubectl apply -f https://raw.githubusercontent.com/<org>/certupload/<tag>/dist/install.yaml
```

### 方式二：Helm Chart

适合需要自定义配置的场景。

```bash
# 生成 Chart（首次）
kubebuilder edit --plugins=helm/v2-alpha

# 安装
helm install certupload ./dist/chart/ \
  --namespace certupload --create-namespace \
  --set controllerManager.manager.image.repository=<your-registry>/certupload \
  --set controllerManager.manager.image.tag=<tag>

# 卸载
helm uninstall certupload -n certupload
```

> **注意：** 更新 CRD 或新增 webhook 后需重新生成 Chart（`--force`），并手动恢复自定义 values。

## 故障排查

### 常见问题

#### 1. CRD 未创建

```bash
# 检查 CRD 是否存在
kubectl get crd certuploads.aliyun.weyundong.com

# 手动安装 CRD
make install
```

#### 2. 控制器 Pod 异常

```bash
# 查看 Pod 状态
kubectl get pods -n certupload

# 查看 Pod 详情
kubectl describe pod -n certupload -l control-plane=controller-manager

# 查看日志
kubectl logs -n certupload deployment/certupload-controller-manager -c manager

# 查看事件
kubectl get events -n certupload --sort-by='.lastTimestamp'
```

#### 3. 证书同步失败

```bash
# 查看 CertUpload 状态（含 OSS/CDN 独立状态）
kubectl describe certupload <name>

# 查看 OSS 同步状态
kubectl get certupload <name> -o jsonpath='{.status.ossStatus}'

# 查看 CDN 同步状态
kubectl get certupload <name> -o jsonpath='{.status.cdnStatus}'

# 检查 Secret 是否存在
kubectl get secret <secret-name> -n <namespace>

# 验证阿里云凭证
kubectl get secret <credential-secret> -n <namespace> -o yaml

# 查看控制器日志中的错误
kubectl logs -n certupload deployment/certupload-controller-manager -c manager | grep -i error
```

#### 4. RBAC 权限问题

```bash
# 检查 ServiceAccount
kubectl get serviceaccount -n certupload

# 检查 ClusterRole 和 ClusterRoleBinding
kubectl get clusterrole,clusterrolebinding | grep certupload

# 查看 RBAC 配置
kubectl get clusterrole certupload-manager-role -o yaml
```

#### 5. CDN 域名证书配置失败

```bash
# 查看 CDN 错误信息
kubectl get certupload <name> -o jsonpath='{.status.cdnErrorMessage}'

# 确认 CDN 域名已在阿里云 CDN 控制台添加
# 检查证书 ID 是否有效
kubectl get certupload <name> -o jsonpath='{.status.casCertificateId}'

# 验证区域配置（CDN 为全球服务，但 CAS 需要正确区域）
kubectl get certupload <name> -o jsonpath='{.spec.region}'
```

### 调试技巧

```bash
# 增加日志级别
kubectl set env deployment/certupload-controller-manager -n certupload LOG_LEVEL=debug

# 查看资源 YAML
kubectl get certupload <name> -o yaml

# 查看控制器配置
kubectl get configmap -n certupload

# 进入容器调试
kubectl exec -it -n certupload deployment/certupload-controller-manager -c manager -- /bin/sh
```

## 贡献指南

我们欢迎社区贡献！参与贡献请遵循以下流程：

1. **Fork 仓库** - 点击右上角 Fork 按钮
2. **创建分支** - `git checkout -b feature/amazing-feature`
3. **提交更改** - `git commit -m 'Add some amazing feature'`
4. **推送分支** - `git push origin feature/amazing-feature`
5. **创建 PR** - 在 GitHub 上发起 Pull Request

### 贡献规范

- ✅ 遵循 [Go 代码规范](https://golang.org/doc/effective_go)
- ✅ 添加必要的单元测试和集成测试
- ✅ 更新相关文档和注释
- ✅ 确保所有测试通过：`make test`
- ✅ 代码风格检查：`make lint`

## 更多资源

- 📖 [Kubebuilder 官方文档](https://book.kubebuilder.io/)
- 🔧 [controller-runtime GitHub](https://github.com/kubernetes-sigs/controller-runtime)
- 📚 [Kubernetes API 约定](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)
- 🛠️ [Operator 模式](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)

## 许可证

Copyright 2026

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
