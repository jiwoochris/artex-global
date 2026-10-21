# ScopeWeaver v0.7.0 — OpenCode Go provider support

## Added

- Add the `opencode-go` provider alias, using OpenAI Chat Completions with `OPENCODE_API_KEY`, default endpoint `https://opencode.ai/zen/go/v1` and default model `glm-5.3-flash`. A generic configured API key takes precedence. See [#47](https://github.com/cskwork/scopeweaver/pull/47).
- Add an OpenCode Go configuration template with reasoning effort `max`, output limit `16384` sent as `max_tokens`, context window `0` (200K default), and the required `x-opencode-session` header. Template labels and guidance ship in English, Korean and Simplified Chinese. See [provider setup and subscription terms](../docs/llm-providers.md#opencode-go-opencode-subscription-gateway).
- Company `domain` scope now covers its own name and descendants; `host` covers exactly one name, entered as `host:<name>` in the scope editor. Task host scope links that exact hostname as a subdomain; an apex host reuses the root-domain asset. See [domain matching](https://github.com/cskwork/scopeweaver/commit/99b424a), [scope editor](https://github.com/cskwork/scopeweaver/commit/6d70637) and [task registration](https://github.com/cskwork/scopeweaver/commit/e5a2bc6).

## Fixed

- Trim file-configured provider names before credential selection so padded `opencode-go` values cannot inherit `OPENAI_API_KEY`. An explicitly empty generic API-key environment variable still overrides file credentials and suppresses provider-key fallback; saving a new UI profile with a blank key does not import environment credentials.
- Keep the required provider session-header default through connection tests, legacy default-profile saves and reloads, while preserving explicit request or saved-profile header precedence in connection tests. Send `User-Agent: scopeweaver` on LLM requests when no User-Agent is supplied, preserving explicitly configured custom or blank headers.
- Drain admitted chat and side work before closing stores and report final flush failures. Preserve complete proxy URLs and reject transcript paths that point to files. See [runtime fixes](https://github.com/cskwork/scopeweaver/commit/2273d50).

## Upgrade and verification

Back up the PostgreSQL database and data directory before upgrading. Startup updates the `company_scope` kind and payload constraints to accept `host` rules; the OpenCode Go changes themselves add no schema changes. Upgrades from versions before v0.6.0 still apply the [v0.6.0 migration and backup requirements](https://github.com/cskwork/scopeweaver/releases/tag/v0.6.0). Stop the previous server before replacement and keep one active server per database with exclusive ownership of its data directory.

Candidate `dabda09` passed all seven required hosted checks: branch policy, frontend, agent adapter, backend, Windows, chat browser and smoke. Local verification passed all 22 Go packages, 72 frontend tests, TypeScript checks, static frontend and embedded-binary builds, and `go vet`.

A real browser exercised template selection, model loading, connection testing, save/reopen, activation and two chat turns against a simulated gateway. Wire checks confirmed the same conversation session, application User-Agent, effort `max` and output limit `16384`. Live OpenCode inference and account entitlement remain unverified because no OpenCode Go key was available. Confirm that your subscription permits use from ScopeWeaver before saving a key. Local macOS full smoke did not pass: native clipboard steps timed out, with an identical failure reproduced on the untouched PR baseline. Hosted Linux/browser and Windows checks passed. Simulated-gateway evidence does not establish live gateway compatibility; release packaging and publication are separate delivery steps.

## 한국어

### 추가

- OpenAI Chat Completions 형식의 `opencode-go` 제공자 별칭을 추가했습니다. `OPENCODE_API_KEY`를 사용하고 기본 엔드포인트는 `https://opencode.ai/zen/go/v1`, 기본 모델은 `glm-5.3-flash`입니다. 별도로 설정한 공통 API 키가 우선합니다. [#47](https://github.com/cskwork/scopeweaver/pull/47)을 참고하세요.
- 추론 강도 `max`, 출력 한도 `16384`(`max_tokens`로 전송), 문맥 창 `0`(기본 200K), 필수 `x-opencode-session` 헤더를 채우는 OpenCode Go 설정 템플릿을 추가했습니다. 템플릿 이름과 안내는 영어·한국어·중국어 간체로 제공합니다. [제공자 설정과 구독 약관 안내](../docs/ko/llm-providers.md)를 참고하세요.
- 회사의 `domain` 범위는 해당 이름과 모든 하위 이름을 포함하고, `host` 범위는 정확히 한 이름만 포함합니다. 범위 편집기에 `host:<이름>`으로 입력할 수 있습니다. 작업의 호스트 범위는 해당 호스트만 하위 도메인 자산으로 연결하며, 최상위 도메인인 호스트는 기존 루트 도메인 자산을 사용합니다. [도메인 일치](https://github.com/cskwork/scopeweaver/commit/99b424a), [범위 편집기](https://github.com/cskwork/scopeweaver/commit/6d70637), [작업 등록](https://github.com/cskwork/scopeweaver/commit/e5a2bc6)을 참고하세요.

### 수정

- 파일에 설정한 제공자 이름의 앞뒤 공백을 제거한 뒤 인증 정보를 선택하여, 공백이 포함된 `opencode-go`가 `OPENAI_API_KEY`를 잘못 사용하지 않도록 했습니다. 공통 API 키 환경 변수를 명시적으로 비우면 파일의 키보다 우선하며 제공자 키로 대체하지 않습니다. 새 UI 프로필의 키를 비워 저장해도 환경 변수의 인증 정보를 가져오지 않습니다.
- 연결 검사, 기존 기본 프로필 저장, 다시 읽기에서 제공자의 필수 세션 헤더 기본값을 유지합니다. 연결 검사에서는 요청에 지정한 헤더나 저장된 프로필의 헤더가 우선합니다. LLM 요청에 User-Agent를 지정하지 않으면 `User-Agent: scopeweaver`를 보내며, 명시적으로 지정한 사용자 정의 헤더나 빈 헤더는 유지합니다.
- 저장소를 닫기 전에 접수한 채팅과 곁가지 작업의 종료를 기다리고, 마지막 저장 실패를 보고합니다. 프록시 URL 전체를 보존하고 파일을 가리키는 대화 기록 경로를 거부합니다. [런타임 수정](https://github.com/cskwork/scopeweaver/commit/2273d50)을 참고하세요.

### 업데이트와 검증

업데이트 전에 PostgreSQL 데이터베이스와 데이터 디렉터리를 백업하세요. 시작 시 `host` 규칙을 허용하도록 `company_scope`의 종류와 내용 제약 조건을 갱신합니다. OpenCode Go 변경 자체에는 스키마 변경이 없습니다. v0.6.0보다 이전 버전에서 업데이트하면 [v0.6.0의 마이그레이션과 백업 안내](https://github.com/cskwork/scopeweaver/releases/tag/v0.6.0)도 적용됩니다. 교체 전에 이전 서버를 중지하고, 데이터베이스마다 서버 하나만 실행하며 데이터 디렉터리를 단독으로 사용하세요.

후보 `dabda09`에서 필수 원격 검사 7개인 브랜치 정책·프런트엔드·에이전트 어댑터·백엔드·Windows·채팅 브라우저·스모크가 모두 통과했습니다. 로컬에서는 Go 패키지 22개, 프런트엔드 테스트 72개, TypeScript 검사, 정적 프런트엔드와 내장 실행 파일 빌드, `go vet`를 통과했습니다.

실제 브라우저에서 모의 게이트웨이를 대상으로 템플릿 선택, 모델 조회, 연결 검사, 저장 후 다시 열기, 활성화, 채팅 두 차례를 확인했습니다. 실제 전송 요청에서 동일한 대화 세션, 앱 User-Agent, 추론 강도 `max`, 출력 한도 `16384`를 확인했습니다. OpenCode Go 키가 없어 실제 OpenCode 추론과 계정의 사용 권한은 검증하지 않았습니다. 키를 저장하기 전에 구독 약관이 ScopeWeaver에서의 사용을 허용하는지 확인하세요. 로컬 macOS 전체 스모크는 통과하지 못했습니다. 실제 클립보드 단계가 시간 초과되었으며, 수정하지 않은 PR 기준 실행 파일에서도 같은 실패를 재현했습니다. 원격 Linux·브라우저 및 Windows 검사는 통과했습니다. 모의 게이트웨이 검증은 실제 게이트웨이 호환성을 입증하지 않으며, 릴리스 패키징과 게시는 별도 전달 단계입니다.

[Compare v0.6.1...v0.7.0](https://github.com/cskwork/scopeweaver/compare/v0.6.1...v0.7.0)
