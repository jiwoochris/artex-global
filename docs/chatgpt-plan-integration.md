# Integração do ARTEX com o plano ChatGPT

## Objetivo

Permitir que uma instalação local do ARTEX use um plano ChatGPT elegível por meio de **Entrar com ChatGPT**, sem exigir uma API key. Essa opção não transforma o ARTEX no Codex: o ARTEX continua responsável por seus agentes, prompts, ferramentas, transcrições e banco de dados; somente a inferência do modelo passa pelo acesso autorizado da conta ChatGPT.

O recurso oficial ainda é uma prévia e tem limitações próprias. As referências normativas são [Token sharing for open-source agents](https://developers.openai.com/siwc/token-sharing-open-source), [Sign-in flow](https://developers.openai.com/siwc/token-sharing-open-source/sign-in), [Accounts and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions), [Models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference) e [Preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).

## Arquitetura

1. O administrador inicia o login em **Sistema → LLM**.
2. O backend gera `state`, `nonce` e PKCE S256, mantendo a tentativa somente em memória.
3. O navegador abre a autorização da OpenAI e retorna para um callback IPv4 loopback (`127.0.0.1`).
4. O backend troca o código, valida assinatura, emissor, audiência, expiração e nonce do ID token e confirma o escopo `chatgpt.tokens.use.direct`.
5. Access, refresh e ID tokens são gravados atomicamente em arquivo local `0600`, fora do navegador de arquivos do ARTEX. O refresh token nunca entra no PostgreSQL nem no objeto do provedor.
6. Um perfil `chatgpt-plan` usa o provedor Responses já existente. No limite HTTP, o ARTEX injeta o access token renovado, força `stream:true` e `store:false`, remove campos não aceitos e move as funções para um item `additional_tools` de desenvolvedor.

## Estado implementado

- OAuth/OIDC com PKCE, estado descartável e nonce.
- Validação RS256 usando o JWKS publicado pela OpenAI.
- Persistência local restrita e substituição atômica do arquivo.
- Renovação serializada do refresh token rotativo.
- Revogação e remoção local ao desconectar.
- Descoberta dos modelos visíveis à conta.
- Formato `chatgpt-plan` no banco, failover, teste de conexão e interface.
- Volume persistente `./.secrets:/app/secrets` na instalação Docker Compose.
- Testes do protocolo e da transformação da requisição.

## Como usar

1. Inicie o ARTEX normalmente e abra `http://localhost:8787` (ou `http://127.0.0.1:8787`).
2. Entre como administrador e acesse **Sistema → LLM**.
3. Clique em **Continuar com ChatGPT** e conclua o login na janela aberta.
4. Crie um perfil, escolha **ChatGPT plan (OAuth)** e carregue a lista de modelos.
5. Escolha um modelo, salve o perfil e marque-o como ativo.
6. Use **Testar conexão** antes de iniciar uma tarefa.

O callback oficial é local. Se a interface for acessada por outra máquina, túnel, proxy reverso ou porta não padrão, configure `ARTEX_CHATGPT_REDIRECT_URI` com um URL `http://127.0.0.1:<porta>/api/auth/chatgpt-plan/callback` realmente alcançável pelo navegador que faz o login.

## Armazenamento e operação

- Execução local: o padrão é `chatgpt-plan-auth.json` ao lado da configuração/chave JWT. Pode ser alterado por `ARTEX_CHATGPT_AUTH_FILE`.
- Docker Compose: o arquivo fica em `/app/secrets/chatgpt-plan-auth.json`, persistido no diretório ignorado `./.secrets` do host.
- Faça backup do arquivo apenas em armazenamento secreto. Quem obtiver o refresh token poderá usar a autorização até ela expirar ou ser revogada.
- Desconectar remove as credenciais locais mesmo quando a revogação remota não puder ser confirmada; nesse caso a interface informa o aviso e a conexão também pode ser removida nas configurações do ChatGPT.

## Restrições atuais

- Uma conta ChatGPT conectada por instalação nesta primeira versão.
- O endpoint é fixo no serviço oficial da OpenAI; Base URL e API key não aparecem no perfil.
- A execução é sempre streaming e stateless. Limite de tokens de saída e temperatura não são enviados.
- A disponibilidade, os modelos e os limites de uso são definidos pelo plano ChatGPT e pela prévia da OpenAI, não pelo ARTEX.
- Este mecanismo não contorna limites do plano nem concede acesso à API faturada separadamente.

## Próximos marcos

1. Cache de JWKS com renovação ao encontrar um `kid` desconhecido.
2. Suporte a várias contas/registrations com seleção explícita.
3. Recuperação guiada para `invalid_grant` preservando o client ID emitido.
4. Métricas específicas de sessão, renovação e limites do plano, sem registrar tokens.
5. Teste de integração opt-in contra uma conta real em ambiente local.
