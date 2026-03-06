# certupload

certupload 是一个 Kubernetes Operator，用于自动将 cert-manager 生成的 TLS 证书同步到阿里云 SSL 证书服务（CAS）并更新 OSS 域名证书配置。

## 项目简介

certupload 基于 Kubebuilder 框架构建，提供了一个自定义资源 `CertUpload`，用于管理证书同步流程。当 cert-manager 颁发或更新证书时，Operator 会自动将证书上传到阿里云 SSL 证书服务，并关联到指定的 OSS 存储桶域名，实现 HTTPS 证书的自动续期和部署。

### 核心功能

- 自动同步 cert-manager 生成的证书到阿里云 SSL 证书服务
- 自动更新 OSS 存储桶域名证书配置
- 支持证书自动续期
- 提供完整的状态监控和事件记录
- 支持跨命名空间的证书引用

## 快速开始

### 前置要求

- go 版本 v1.24.6+
- docker 版本 17.03+
- kubectl 版本 v1.11.3+
- 访问 Kubernetes v1.11.3+ 集群的权限

### 部署到集群

**构建并推送镜像到指定的镜像仓库：**

```sh
make docker-build docker-push IMG=<某个镜像仓库>/certupload:tag
```

**注意：** 此镜像应当发布到您指定的个人镜像仓库中。工作环境需要有权从该仓库拉取镜像。如果上述命令不起作用，请确保您拥有该仓库的适当权限。

**将 CRD 安装到集群：**

```sh
make install
```

**使用指定的镜像部署 Manager 到集群：**

```sh
make deploy IMG=<某个镜像仓库>/certupload:tag
```

> **注意：** 如果遇到 RBAC 错误，您可能需要为自己授予 cluster-admin 权限或以管理员身份登录。

**创建解决方案实例**

您可以从 config/sample 应用示例：

```sh
kubectl apply -k config/samples/
```

>**注意：** 确保示例具有默认值以便测试。

### 卸载

**从集群删除实例（CR）：**

```sh
kubectl delete -k config/samples/
```

**从集群删除 API（CRD）：**

```sh
make uninstall
```

**从集群取消部署控制器：**

```sh
make undeploy
```

## 本地测试指南

### 使用 Kind 进行本地测试

本项目支持使用 [Kind](https://kind.sigs.k8s.io/) 在本地 Kubernetes 集群中进行测试。

#### 前置要求

- 安装 [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- 安装 [kubectl](https://kubernetes.io/docs/tasks/tools/)

#### 测试步骤

##### 1. 构建镜像并加载到 Kind

```bash
# 设置镜像名称（本地测试不需要推送到远程仓库）
export IMG=certupload:test

# 构建镜像并加载到现有的 Kind 集群
docker build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t ${IMG} .

kind load docker-image ${IMG} --name kind
```

##### 2. 部署 CRD 和控制器

```bash
# 更新镜像配置
cd config/manager && kustomize edit set image controller=${IMG} && cd ../..

# 部署到 Kind 集群
kustomize build config/default | kubectl apply --context kind-kind -f -
```

##### 3. 验证部署

```bash
# 查看 Pod 状态
kubectl --context kind-kind get pods -n certupload-system

# 等待 Pod 就绪
kubectl --context kind-kind wait --for=condition=ready pod -l control-plane=controller-manager -n certupload-system --timeout=120s

# 查看日志
kubectl --context kind-kind logs -n certupload-system deployment/certupload-controller-manager -c manager -f
```

##### 4. 安装 cert-manager（必需）

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.0/cert-manager.yaml

# 等待 cert-manager 就绪
kubectl wait --for=condition=ready pod -l app=cert-manager -n cert-manager --timeout=120s
```

##### 5. 创建测试资源

```bash
# 创建阿里云凭证 Secret
kubectl apply -f config/samples/secret-sample.yaml

# 创建 CertUpload 资源
kubectl apply -f config/samples/certupload-sample.yaml
```

##### 6. 查看测试结果

```bash
# 查看 CertUpload 资源状态
kubectl --context kind-kind get certupload -A

# 查看详细状态
kubectl --context kind-kind describe certupload certupload-sample

# 查看事件
kubectl --context kind-kind get events -n default --sort-by='.lastTimestamp'

# 查看控制器日志
kubectl --context kind-kind logs -n certupload-system deployment/certupload-controller-manager -c manager -f
```

##### 7. 清理资源

```bash
# 删除测试资源
kubectl delete -f config/samples/certupload-sample.yaml
kubectl delete -f config/samples/secret-sample.yaml

# 卸载控制器（可选）
kustomize build config/default | kubectl delete -f -
```

#### 使用 Makefile 快捷命令

项目提供了便捷的 Makefile 目标用于 Kind 测试：

```bash
# 设置镜像名称
export IMG=certupload:test

# 创建 Kind 集群
make kind-create KIND_CLUSTER_NAME=certupload-test

# 构建并加载镜像到 Kind
make kind-load KIND_CLUSTER_NAME=certupload-test

# 部署控制器到 Kind 集群
make kind-deploy KIND_CLUSTER_NAME=certupload-test

# 一键测试（创建集群、构建、部署）
make kind-test KIND_CLUSTER_NAME=certupload-test

# 查看控制器日志
make kind-logs KIND_CLUSTER_NAME=certupload-test

# 清理 Kind 集群
make kind-clean KIND_CLUSTER_NAME=certupload-test
```

#### 常见问题排查

```bash
# 查看 CRD 是否已创建
kubectl get crd certuploads.aliyun.weyundong.com

# 查看控制器 Deployment
kubectl get deployment -n certupload-system

# 查看控制器 Pod 详情
kubectl describe pod -n certupload-system -l control-plane=controller-manager
```

## 项目分发

以下选项用于发布并向用户提供此解决方案。

### 方式一：提供包含所有 YAML 文件的安装包

1. 为已构建并发布到镜像仓库的镜像构建安装程序：

```sh
make build-installer IMG=<某个镜像仓库>/certupload:tag
```

**注意：** 上述 makefile 目标在 dist 目录中生成一个 'install.yaml' 文件。该文件包含使用 Kustomize 构建的所有资源，这些资源是在没有依赖项的情况下安装此项目所必需的。

2. 使用安装程序

用户只需运行 'kubectl apply -f <YAML 安装包的 URL>' 即可安装项目，例如：

```sh
kubectl apply -f https://raw.githubusercontent.com/<组织>/certupload/<标签或分支>/dist/install.yaml
```

### 方式二：提供 Helm Chart

1. 使用可选的 helm 插件构建 chart

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. 查看 'dist/chart' 下生成的 chart，用户可以从那里获取此解决方案。

**注意：** 如果您更改项目，需要使用上述相同命令更新 Helm Chart 以同步最新更改。此外，如果您创建 webhook，则需要使用带有 '--force' 标志的上述命令，并手动确保之后手动重新应用之前添加到 'dist/chart/values.yaml' 或 'dist/chart/manager/manager.yaml' 的任何自定义配置。

## 开发指南

### 运行单元测试

```bash
make test
```

### 代码检查

```bash
# 运行 lint 检查
make lint

# 自动修复代码风格
make lint-fix
```

### 生成本地文档

```bash
# 生成 CRD 和 RBAC 清单
make manifests

# 生成 DeepCopy 方法
make generate
```

## 贡献指南

我们欢迎社区贡献！请遵循以下步骤：

1. Fork 本仓库
2. 创建功能分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'Add some amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 开启 Pull Request

请确保：
- 代码遵循 Go 编码规范
- 添加/更新单元测试和 e2e 测试
- 更新相关文档

**注意：** 运行 `make help` 获取所有可能的 `make` 目标的更多信息

更多信息可以通过 [Kubebuilder 文档](https://book.kubebuilder.io/introduction.html) 获取

## 许可证

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
