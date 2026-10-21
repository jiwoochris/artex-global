# AI 에이전트 어댑터

Claude Code와 Codex는 로컬 stdio MCP 서버로, Pi는 전용 확장으로 ScopeWeaver에 요청할 수 있습니다. 세 클라이언트 모두 기존 인증 API를 사용하여 작업 목록·진행 상태·커버리지·발견 사항을 읽습니다. 환경 변수 `SCOPEWEAVER_ALLOW_WRITES=true`를 설정하면 작업 생성과 일시정지·재개도 사용할 수 있습니다.

이 어댑터는 코딩 에이전트가 ScopeWeaver를 제어하는 기능입니다. ScopeWeaver 내부의 플래너·워커를 코딩 에이전트로 교체하거나, 코딩 에이전트 구독을 모델 API 키로 바꾸는 기능은 아닙니다.

Node.js 22 이상과 실행 중인 ScopeWeaver가 필요합니다. `adapters/agent`에서 `npm ci`를 실행하고 `SCOPEWEAVER_URL`과 기존 관리자 비밀번호(`SCOPEWEAVER_PASSWORD`) 또는 로그인 토큰(`SCOPEWEAVER_TOKEN`)을 환경 변수로 설정하세요. 비밀번호나 토큰을 저장소에 커밋하지 마세요. `SCOPEWEAVER_LANGUAGE=ko`로 한국어 응답을 요청할 수 있습니다.

- Claude Code: `claude mcp add --transport stdio scopeweaver -- node /절대경로/scopeweaver/adapters/agent/src/mcp.js`
- Codex: `codex mcp add scopeweaver -- node /절대경로/scopeweaver/adapters/agent/src/mcp.js`
- Pi: `pi -e /절대경로/scopeweaver/adapters/agent/pi-extension.js`

작업을 생성하면 백엔드 모델 설정에 따라 즉시 실행이 시작될 수 있습니다. 프로젝트의 로컬 격리 환경 사용 제한을 지키고 대상 범위를 먼저 확인하세요. 생성 응답은 작업 등록을 뜻하며 검사 완료를 뜻하지 않습니다. 요청 실패는 명확한 오류로 반환하고, 쓰기 요청을 자동 재시도하지 않습니다.

설정, 도구 목록, 오류 처리와 실제 백엔드를 사용하는 E2E 테스트 방법은 [어댑터 문서](../../adapters/agent/README.md)를 참고하세요.

관리자가 직접 관리하는 로컬 설치에서는 `node adapters/agent/issue-token.js --root <저장소 경로>`로 비밀번호를 몰라도 로컬 키 자료에서 로그인 토큰을 발급할 수 있습니다. 발급된 토큰은 비밀번호와 동일하게 취급하고 저장소에 커밋하지 마세요. 비밀번호를 변경하면 기존 토큰은 모두 무효가 됩니다.
