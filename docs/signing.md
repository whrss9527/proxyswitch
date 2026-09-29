# 代码签名

没有数字签名的程序第一次运行时，Windows 的 SmartScreen 会弹出「Windows 已保护你的电脑」，杀毒软件也更容易误报。给 `ProxySwitch.exe`、`ProxySwitch-arm64.exe` 和安装包 `ProxySwitch-Setup.exe` 签名后，属性里能看到发布者，下载后一般可以直接运行。

签名用的是 [SignPath](https://signpath.io) 给开源项目的免费签名：证书由 SignPath Foundation 签发，签名里的发布者显示为「SignPath Foundation」。

发布流程已经接好了。仓库里配置了下面这些变量时，发版会自动签名；没配置时照旧发布没有签名的版本，工作流里会有一条提醒。

## 申请

1. 在 [signpath.org](https://signpath.org) 申请开源项目的免费签名，项目地址填 `https://github.com/whrss9527/proxyswitch`。SignPath 主要看这几点，本项目都已满足：
   - 开源许可证：MIT（`LICENSE`）。
   - 程序在 GitHub Actions 上从源码编译（`.github/workflows/release.yml`）。
   - 项目主页写了代码签名策略和隐私说明：README 的「[代码签名](../README.md#代码签名)」一节。
   - 维护者的 GitHub 和 SignPath 账号开启两步验证。
2. 审核由 SignPath 决定，通过后会开通组织和项目。

## 在 SignPath 里配置

1. 按 SignPath 的说明把 GitHub 仓库连接为可信的构建系统（Trusted Build System 选 GitHub.com）。
2. 项目（Project）的 slug 记下来，例如 `proxyswitch`；签名策略（Signing Policy）用发布签名的那个，例如 `release-signing`。
3. 建两个 Artifact Configuration，slug 必须是 `programs` 和 `installer`（发布流程里写死了这两个名字）：

   `programs`（两个 exe）：

   ```xml
   <?xml version="1.0" encoding="utf-8"?>
   <artifact-configuration xmlns="http://signpath.io/artifact-configuration/v1">
     <zip-file>
       <pe-file path="ProxySwitch.exe">
         <authenticode-sign />
       </pe-file>
       <pe-file path="ProxySwitch-arm64.exe">
         <authenticode-sign />
       </pe-file>
     </zip-file>
   </artifact-configuration>
   ```

   `installer`（安装包）：

   ```xml
   <?xml version="1.0" encoding="utf-8"?>
   <artifact-configuration xmlns="http://signpath.io/artifact-configuration/v1">
     <zip-file>
       <pe-file path="ProxySwitch-Setup.exe">
         <authenticode-sign />
       </pe-file>
     </zip-file>
   </artifact-configuration>
   ```

4. 建一个 CI 用的用户，生成 API Token，给它提交签名请求的权限。

## 在 GitHub 仓库里配置

Settings → Secrets and variables → Actions：

| 类型 | 名字 | 值 |
| --- | --- | --- |
| Secret | `SIGNPATH_API_TOKEN` | 上面生成的 API Token |
| Variable | `SIGNPATH_ORGANIZATION_ID` | SignPath 组织的 ID |
| Variable | `SIGNPATH_PROJECT_SLUG` | 项目的 slug，例如 `proxyswitch` |
| Variable | `SIGNPATH_SIGNING_POLICY_SLUG` | 签名策略的 slug，例如 `release-signing` |

## 发版时

发版流程（合并进 main 后自动运行，或者在 Actions 页面手动运行 release）会提交两次签名请求：先签两个 exe，再用签好的 exe 打安装包，签安装包。发布签名要人工批准时，在 SignPath 的网站上（或者按它的邮件）批准这两次请求；工作流每次最多等两小时，超时就不发布，在 Actions 页面重新运行 release 即可。

签名之后，SmartScreen 按证书积累的信誉判断要不要提醒。SignPath Foundation 的证书有很多开源项目在用，一般不会再拦，但微软没有保证每个新版本第一时间都不提示。
