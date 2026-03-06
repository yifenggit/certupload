# certupload

<p align="center">
  <strong>Kubernetes Operator for Automatic Certificate Synchronization to Alibaba Cloud SSL Services</strong>
</p>

<p align="center">
  <a href="#简介">简介</a> •
  <a href="#快速开始">快速开始</a> •
  <a href="#本地测试">本地测试</a> •
  <a href="#部署指南">部署指南</a> •
  <a href="#开发指南">开发指南</a>
</p>

---

## 简介

`certupload` 是一个基于 Kubebuilder 框架构建的 Kubernetes Operator，用于自动化管理 TLS 证书的生命周期。它能够将 cert-manager 生成的证书自动同步到阿里云 SSL 证书服务（CAS），并配置 OSS 存储桶域名证书，实现证书的自动续期和部署。

### 核心功能

- 🔄 **自动同步** - 监听 cert-manager 颁发的证书，自动上传至阿里云 SSL 证书服务
- 🚀 **自动部署** - 自动关联证书到指定的 OSS 存储桶域名，完成 HTTPS 配置
- ♻️ **证书续期** - 支持 cert-manager 证书自动续期，无缝更新云端证书
- 📊 **状态监控** - 提供完整的 CR 状态和事件记录，便于监控和排错
- 🔗 **跨命名空间** - 支持引用不同命名空间中的 Secret 资源

### 工作原理

```
┌──────────────┐      ┌──────────────┐      ┌─────────────────┐
│  cert-manager │─────▶│  CertUpload  │─────▶│ Alibaba Cloud   │
│   (颁发证书)   │      │   Operator   │      │  SSL Services   │
└──────────────┘      └──────────────┘      │  + OSS CDN      │
                             │               └─────────────────┘
                             │
                             ▼
                      ┌──────────────┐
                      │  Status &    │
                      │   Events     │
                      └──────────────┘
```

## 快速开始

### 前置要求

| 工具 | 版本要求 |
|------|---------|
| Go | v1.24.6+ |
| Docker | 17.03+ |
| kubectl | v1.11.3+ |
| Kubernetes | v1.11.3+ |

### 安装部署

#### 1. 构建并推送镜像

```bash
# 设置镜像地址
export IMG=<your-registry>/certupload:<tag>

# 构建并推送镜像
make docker-build docker-push IMG=$IMG
```

> **注意：** 确保目标集群能够访问镜像仓库。如遇权限问题，请检查仓库访问权限或使用镜像拉取密钥。

#### 2. 安装 CRD

```bash
make install
```

#### 3. 部署控制器

```bash
make deploy IMG=$IMG
```

> **提示：** 如遇 RBAC 权限错误，请确保拥有 `cluster-admin` 权限或联系集群管理员。

#### 4. 创建示例资源

```bash
# 应用示例配置
kubectl apply -k config/samples/

# 查看资源状态
kubectl get certupload -A
```

### 卸载

```bash
# 1. 删除 CR 实例
kubectl delete -k config/samples/

# 2. 删除 CRD
make uninstall

# 3. 卸载控制器
make undeploy
```

## 本地测试

本项目推荐使用 [Kind](https://kind.sigs.k8s.io/) 进行本地开发和测试。

### 使用 Makefile 快速测试

```bash
# 一键测试：创建集群、构建、部署
make kind-test IMG=certupload:test

# 或者分步执行：
export IMG=certupload:test
make kind-create          # 创建 Kind 集群
make kind-load            # 构建并加载镜像
make kind-deploy          # 部署控制器
```

### 手动测试步骤

#### 1. 准备 Kind 集群

```bash
# 创建集群
kind create cluster --name certupload-test

# 构建镜像
docker build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t certupload:test .

# 加载镜像到集群
kind load docker-image certupload:test --name certupload-test
```

#### 2. 部署控制器

```bash
# 配置镜像地址
cd config/manager && kustomize edit set image controller=certupload:test && cd ../..

# 部署到集群
kustomize build config/default | kubectl apply --context kind-certupload-test -f -

# 验证部署状态
kubectl --context kind-certupload-test get pods -n certupload-system
```

#### 3. 安装 cert-manager（必需）

```bash
# 安装 cert-manager
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.0/cert-manager.yaml

# 等待就绪
kubectl wait --for=condition=ready pod -l app=cert-manager -n cert-manager --timeout=120s
```

#### 4. 创建测试资源

```bash
# 创建阿里云凭证 Secret
kubectl apply -f config/samples/secret-sample.yaml

# 创建 CertUpload 资源
kubectl apply -f config/samples/certupload-sample.yaml
```

#### 5. 验证结果

```bash
# 查看 CertUpload 状态
kubectl get certupload -A

# 查看详细信息
kubectl describe certupload certupload-sample

# 查看事件
kubectl get events -n default --sort-by='.lastTimestamp'

# 查看控制器日志
kubectl logs -n certupload-system deployment/certupload-controller-manager -c manager -f
```

#### 6. 清理资源

```bash
# 删除测试资源
kubectl delete -f config/samples/certupload-sample.yaml
kubectl delete -f config/samples/secret-sample.yaml

# 卸载控制器
kustomize build config/default | kubectl delete -f -

# 删除集群
kind delete cluster --name certupload-test
```

### 常用命令速查

| 命令 | 说明 |
|------|------|
| `make kind-create` | 创建 Kind 集群 |
| `make kind-load` | 构建并加载镜像 |
| `make kind-deploy` | 部署控制器 |
| `make kind-test` | 一键完整测试 |
| `make kind-logs` | 查看控制器日志 |
| `make kind-clean` | 清理 Kind 集群 |

## 部署指南

### 方式一：YAML 安装包（推荐）

适合快速部署，用户只需 `kubectl apply` 即可完成安装。

#### 1. 生成安装包

```bash
make build-installer IMG=<your-registry>/certupload:<tag>
```

这将在 `dist/install.yaml` 生成包含所有必需资源的单一 YAML 文件。

#### 2. 分发安装

用户可以通过以下方式安装：

```bash
# 从远程 URL 安装
kubectl apply -f https://raw.githubusercontent.com/<org>/certupload/<tag>/dist/install.yaml

# 或从本地文件安装
kubectl apply -f dist/install.yaml
```

### 方式二：Helm Chart

适合需要自定义配置的场景。

#### 1. 生成 Helm Chart

```bash
kubebuilder edit --plugins=helm/v2-alpha
```

生成的 Chart 位于 `dist/chart/` 目录。

#### 2. 使用 Helm 部署

**开发环境：**

```bash
# 设置镜像地址
export IMG=<your-registry>/certupload:<tag>

# 部署
make helm-deploy IMG=$IMG

# 查看状态
make helm-status

# 卸载
make helm-uninstall
```

**生产环境：**

```bash
# 安装
helm install certupload ./dist/chart/ \
  --namespace certupload-system \
  --create-namespace \
  --set controllerManager.manager.image.repository=<your-registry>/certupload \
  --set controllerManager.manager.image.tag=<tag>

# 自定义配置
helm install certupload ./dist/chart/ \
  --namespace certupload-system \
  --set controllerManager.replicas=3 \
  --set controllerManager.resources.limits.memory=512Mi
```

> **注意：** 更新项目后需要重新生成 Helm Chart。如创建了 webhook，需使用 `--force` 参数，并手动恢复自定义配置。

#### 3. Helm 常用操作

```bash
make helm-status    # 查看发布状态
make helm-history   # 查看历史版本
make helm-rollback  # 回滚到上一版本
make helm-uninstall # 卸载发布
```

## 开发指南

### 项目结构

```
certupload/
├── api/                    # CRD API 定义
│   └── v1/                 # API 版本
├── cmd/                    # 程序入口
│   └── main.go
├── config/                 # Kubernetes 配置
│   ├── crd/               # CRD 定义
│   ├── rbac/              # RBAC 规则
│   ├── samples/           # 示例资源
│   └── default/           # 默认配置
├── internal/              # 内部实现
│   ├── controller/        # 控制器逻辑
│   └── webhook/           # Webhook 实现（可选）
├── dist/                  # 发布产物
│   └── install.yaml       # 安装包
├── test/                  # 测试代码
├── Dockerfile             # 容器镜像定义
├── Makefile               # 构建脚本
└── PROJECT                # Kubebuilder 项目元数据
```

### 开发工作流

```bash
# 1. 修改 API 定义（api/v1/*_types.go）
# 2. 生成 CRD 和 DeepCopy 方法
make manifests generate

# 3. 修改控制器逻辑（internal/controller/*.go）
# 4. 运行测试
make test

# 5. 代码风格检查
make lint-fix

# 6. 本地运行
make run
```

### 运行测试

```bash
# 单元测试
make test

# 端到端测试（需要 Kind 集群）
make test-e2e
```

### 代码质量

```bash
# 代码检查
make lint

# 自动修复
make lint-fix

# 格式化代码
make fmt
```

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
kubectl get pods -n certupload-system

# 查看 Pod 详情
kubectl describe pod -n certupload-system -l control-plane=controller-manager

# 查看日志
kubectl logs -n certupload-system deployment/certupload-controller-manager -c manager

# 查看事件
kubectl get events -n certupload-system --sort-by='.lastTimestamp'
```

#### 3. 证书同步失败

```bash
# 查看 CertUpload 状态
kubectl describe certupload <name>

# 检查 Secret 是否存在
kubectl get secret <secret-name> -n <namespace>

# 验证阿里云凭证
kubectl get secret <credential-secret> -n <namespace> -o yaml

# 查看控制器日志中的错误
kubectl logs -n certupload-system deployment/certupload-controller-manager -c manager | grep -i error
```

#### 4. RBAC 权限问题

```bash
# 检查 ServiceAccount
kubectl get serviceaccount -n certupload-system

# 检查 ClusterRole 和 ClusterRoleBinding
kubectl get clusterrole,clusterrolebinding | grep certupload

# 查看 RBAC 配置
kubectl get clusterrole certupload-manager-role -o yaml
```

### 调试技巧

```bash
# 增加日志级别
kubectl set env deployment/certupload-controller-manager -n certupload-system LOG_LEVEL=debug

# 查看资源 YAML
kubectl get certupload <name> -o yaml

# 查看控制器配置
kubectl get configmap -n certupload-system

# 进入容器调试
kubectl exec -it -n certupload-system deployment/certupload-controller-manager -c manager -- /bin/sh
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
