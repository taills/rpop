import CopyableCode from './CopyableCode.jsx'
import { GuideOutcome, GuideStep } from './NodeBootstrapParts.jsx'
import { dockerComposeEnvFile, dockerComposeFile, dockerComposeLogsCommand, dockerComposeUpCommand } from '@/nodeBootstrap.js'

// NodeBootstrapComposeTab keeps the join token out of docker-compose.yml itself (so that file stays safe to
// commit) by reading it from a sibling .env file instead — the same split deploy/docker-compose.example.yml
// already uses for its own secrets.
export default function NodeBootstrapComposeTab({ controllerURL, token, image }) {
  const compose = dockerComposeFile({ image, controllerURL })
  const env = dockerComposeEnvFile({ joinToken: token })

  return (
    <div>
      <GuideStep n={1} title="创建 .env（与 docker-compose.yml 放在同一目录）">
        <CopyableCode label=".env" code={env} />
      </GuideStep>

      <GuideStep n={2} title="创建 docker-compose.yml">
        <CopyableCode label="docker-compose.yml" code={compose} />
      </GuideStep>

      <GuideStep n={3} title="启动并查看日志">
        <CopyableCode label="启动" code={dockerComposeUpCommand()} />
        <CopyableCode label="查看日志" code={dockerComposeLogsCommand()} />
      </GuideStep>

      <GuideOutcome tokenNote="注册成功后，清空或删除 .env 中的 RPOP_JOIN_TOKEN 一行，再执行 docker compose up -d 重建容器——节点身份已保存在数据卷里，不会受影响。" />
    </div>
  )
}
