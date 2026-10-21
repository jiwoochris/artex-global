# 변경 이력

한국어 · [English](CHANGELOG.en.md) · [中文(원본·상류)](CHANGELOG.zh.md)

이 문서는 ARTEX 글로벌판(이 포크)이 상류 저장소에 더한 변경을 기록합니다. 형식은 [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) 를 참고합니다.

상류 ARTEX 프로젝트의 버전별 릴리스 이력(0.3.x 이하)과 기여자 목록은 원본 중국어 그대로 [`CHANGELOG.zh.md`](CHANGELOG.zh.md) 에 보존했습니다. 상류 변경과 대조하기 쉽도록 `README.zh.md` 와 같은 방식으로 원문을 그대로 남깁니다. 각 변경의 자세한 내용과 근거는 저장소 커밋 이력에서 확인할 수 있습니다.

## [Unreleased]

### 추가

- **LLM 공급자로 ChatGPT 로그인을 추가했습니다.** 지원되는 로컬 사용자는 API 키 없이 ARTEX 에 ChatGPT 플랜 사용 권한을 부여하고, 계정에 보이는 모델을 선택하며, 시스템 → LLM 에서 연결을 해제할 수 있습니다. OpenAI 의 오픈소스 OAuth/OIDC 프로필에 맞춰 PKCE 를 사용하고 서명된 ID 토큰과 플랜 사용 범위를 검증하며, 회전형 토큰을 갱신하고 소유자 전용 권한으로 자격 증명을 원자적으로 저장하며, Responses API 요청을 플랜 기반 스트리밍 계약에 맞게 변환합니다.

### 현지화 (i18n)

- **스페인어 문서를 추가했습니다.** 영어 README 를 기준으로 `README.es.md` 를 자연스러운 스페인어로 옮기고, 다섯 README(영어·한국어·중국어·스페인어·일본어)의 언어 내비게이션에 스페인어 링크를 넣었습니다. 코드·명령·페이로드·URL·식별자는 원문을 보존하고 설명 텍스트만 번역했습니다.
- **일본어(ja)를 다국어 스택 전체에 추가했습니다.** 앱의 지원 언어에 일본어를 더해 설정 화면에서 전환할 수 있습니다. UI 메시지(`web/messages/ja.json`)·날짜 로케일(`ja-JP`)·에이전트 출력 언어·설정과 서버 메시지 카탈로그(`config/msg_i18n.go`·`server/errmsg_catalog.go`)를 일본어로 번역했고, 에이전트의 내부 추론 프롬프트는 다른 언어와 같은 원칙으로 원어를 유지합니다. 문서는 `README.ja.md` 를 추가하고 다섯 README 의 언어 내비게이션과 지원 언어 안내를 일본어를 포함하도록 갱신했습니다.

### 수정

- 작업 보고서 로딩 실패를 빈 보고서와 구분하고 다시 시도 버튼을 제공합니다. 작업을 전환하면 이전 요청의 늦은 응답이 새 보고서를 덮어쓰지 않습니다.

## [0.3.15-global.1] · 2026-10-14 · ARTEX-global 전환 릴리스

한국어판(`0.3.15-ko.1`)을 전 세계 방어자·연구자용 다국어판 ARTEX-global 로 전환한 릴리스입니다. 상류 ARTEX 0.3.15 를 바탕으로 하며, 에이전트 기능은 바꾸지 않았습니다.

### 정체성 · 다국어 (ARTEX-global 전환)

- **ARTEX 한국어판을 ARTEX 글로벌판으로 전환했습니다.** 정체성을 특정 지역판이 아니라 전 세계 방어자·연구자가 접근하는 다국어판으로 바꿉니다. 에이전트의 기능은 바꾸지 않고 다국어화·문서 범위만 손댔습니다.
  - **기본 언어를 한국어에서 영어로 바꿨습니다.** 사용자에게 보이는 출력의 기본값을 영어로 두고(설정 계층 `config.DefaultLanguage`, 웹 `DEFAULT_LOCALE`, `config.example.json`, `.env.example` 주석), 한국어·중국어·스페인어는 설정 화면에서 전환합니다. 에이전트 패키지 단독 테스트의 폴백(`agent.defaultOutputLang`)과 한국어 골든 테스트는 그대로 두어 회귀를 막았습니다.
  - **정체성 문구를 글로벌판으로 다시 썼습니다.** README(한국어·영어·중국어)·CONTRIBUTING·SECURITY·메인테이너 런북·이슈/PR 템플릿의 "한국어판" 표기를 "글로벌판"으로 바꾸고, 영어 README 의 "왜 한국어판인가" 절과 그 앵커(`#why-a-korean-edition` → `#why-a-global-edition`)를 함께 고쳤습니다. 저장소 슬러그와 이미지 경로는 이 릴리스의 아래 '저장소 리네임' 항목에서 함께 `artex-global` 로 바꿨습니다.

### 첫 화면·스크린샷 영어화

- **GitHub 기본 README 를 영어로 올렸습니다.** 영어 문서를 `README.md`(저장소 첫 화면)로, 한국어 본문을 `README.ko.md` 로 옮기고, 루트 문서들이 최상위 README 를 가리키는 링크·앵커를 영어=`README.md`·한국어=`README.ko.md` 로 재배선했습니다. 탐지 규칙 폴더 안의 README 링크는 그대로 둡니다.
- **미리 보기 스크린샷을 영어로 교체했습니다.** 목 UI 를 기본 영어로 다시 캡처해 `screenshots/en/`(dashboard·findings·chat)으로 넣고, 구버전 한국어 캡처(`screenshots/ko/`)는 제거했습니다.
- **화면 날짜를 앱 언어에 맞췄습니다.** 하드코딩된 `ko-KR` 로케일 18곳을 `web/src/i18n/datetime.ts` 의 `getDateLocale()`(표시 언어→BCP47)로 바꿔, 기본 영어에서 날짜가 영어로 나옵니다.
- **데모 데이터를 영어로 옮겼습니다.** 목 데이터(`web/src/lib/mock/{data,handler}.ts`)의 사용자 노출 한국어 약 220개를 영어로 바꾸고, 명령·페이로드·URL·식별자는 원문을 보존했습니다.

### 저장소 리네임 (artex-ko → artex-global)

- **저장소 이름을 `artex-global` 로 바꿨습니다.** GitHub 슬러그를 `jiwoochris/artex-global` 로(옛 URL 은 GitHub 리다이렉트로 유지), 컨테이너 이미지 경로를 `ghcr.io/jiwoochris/artex-global` 로 바꿨습니다. 배지·클론 URL·`docker-compose.yml`·`release.yml`·`install.sh`·문서·판본 표기·탐지 규칙 author·MISP 이벤트 이름 등 저장소 안 126곳(37개 파일)을 일괄 치환했습니다. 상류 Go 모듈(`github.com/Autumn-27/artex`)과 과거 버전 태그(`0.3.15-ko.1`)는 그대로 둡니다.

### 현지화 (i18n)

- **기본 에이전트의 이름·설명을 언어별로 보여 줍니다.** DB 시드(`db/db.go` 의 `builtinAgents`·`server/finding_retests.go` 의 재검증 에이전트)라 중국어로만 보이던 기본 에이전트(목표 분해·계획 수립·메인·실행·Auto·침투 테스트·취약점 재검증)의 이름·설명과 프롬프트 변수 라벨을, 에이전트 `key` 로 `web/messages/` 에서 찾아 현재 언어로 표시합니다(없으면 DB 값). 사용자가 만든 에이전트는 DB 값을 그대로 보여 줍니다. 에이전트 목록·편집 서랍·대화 화면에 적용했고, 대화에서 생긴 승인·가로채기 기록도 내부 key(`pentest` 등) 대신 에이전트 이름을 보여 줍니다.

### 상류 반영

- **모델 판정(대체 승인)의 token 사용량을 따로 봅니다(상류 0.3.15, #191).** 0.3.15-ko.1 에 빠져 있던 상류 변경입니다. 판정 호출을 `worker=judge` 로 집계하고 `GET /api/intercept/judge/usage` 를 추가했습니다. "시스템 → 명령 가로채기"의 모델 판정 스위치 아래에 호출 수·입력·출력·캐시 읽기/쓰기 누적과 최근 30일 일별 막대 차트가 나옵니다.

### 보안

- **#22 인증 하드닝의 빈틈을 메웠습니다.** 비밀번호 해시 읽기 오류가 토큰 검증과 기동 시 첫 실행 처리에서는 여전히 "비밀번호 없음"으로 접혀, 빈 해시가 캐시되었습니다. 이제 읽기 실패 시 토큰 검증은 거부하고, 기동 시에는 환경 변수 초기화·설정 토큰 발급을 건너뜁니다. `ARTEX_ADMIN_PASSWORD` 초기화도 `InsertSettingIfAbsent` 로 한 번만 저장하고 72바이트 상한을 검사합니다. `/setup` 화면은 상태를 확인하지 못하면 초기화 폼 대신 다시 시도 안내를 띄웁니다(상류 #197 의 화면 변경).

## [0.3.15-ko.1] · 2026-10-12 · 한국어판 첫 릴리스

상류 ARTEX 0.3.15 를 바탕으로 한 한국어판의 첫 릴리스입니다.

### 배포

- **한국어판 Docker 이미지를 발행합니다.** 상류 이미지 `autumn27/artex` 가 Docker Hub 에서 내려가 `docker compose up -d` 빠른 시작이 실패하던 문제를 고쳤습니다. 릴리스 태그마다 `ghcr.io/jiwoochris/artex-global` 에 멀티아키텍처(amd64·arm64) 이미지를 올리고, `docker-compose.yml` 이 이 이미지를 받습니다.

### 보안

- **첫 실행 관리자 계정의 원격 선점을 막았습니다.** 비밀번호가 아직 없는 인스턴스는 `POST /api/auth/init` 을 인증 없이 받아, 포트에 먼저 닿은 누구나 관리자 비밀번호를 정하고 JWT 를 받을 수 있었습니다(비공개 제보: Dangel). 이제 기동 시 서버 콘솔에만 출력되는 설정 토큰(`setup_token`)이 맞아야 초기화되고, 틀린 시도는 횟수 제한에 걸립니다. 무인 배포용으로 `ARTEX_SETUP_TOKEN`·`ARTEX_ADMIN_PASSWORD` 환경 변수를 추가했고, `/setup` 화면에 토큰 입력란을 넣었습니다.
- **기본 바인딩을 127.0.0.1 로 바꿨습니다.** `artex -addr` 기본값이 `:8787` 에서 `127.0.0.1:8787` 로, `docker-compose.yml` 의 8787 게시가 `${ARTEX_BIND:-127.0.0.1}` 로 바뀝니다. 다른 기기에서 접속하던 배포는 `-addr :8787` 또는 `.env` 의 `ARTEX_BIND=0.0.0.0` 을 지정해야 합니다. 비밀번호 없이 루프백이 아닌 주소에 바인딩하면 기동 로그에 경고가 찍힙니다.
- **비밀번호를 바꾸면 기존 토큰이 모두 무효화됩니다.** JWT 서명 키를 비밀번호 해시에서 파생해, 화면 변경·`reset-password.sh`·DB 직접 수정 어느 경로든 이전 세션이 끊깁니다. 업그레이드 직후에도 한 번 다시 로그인해야 합니다. 비밀번호 변경 응답에 새 토큰이 실려 현재 브라우저 세션은 이어집니다.
- **CORS 전면 허용을 허용 목록으로 바꿨습니다.** `Access-Control-Allow-Origin: *` 대신 `ARTEX_CORS_ORIGINS` 에 적힌 출처(기본값은 `next dev` 의 `http://localhost:5173`)에만 헤더를 줍니다. 서버 측 최소 비밀번호 길이(8자) 검사와 초기화·로그인 실패 로그(클라이언트 주소 포함)도 함께 추가했습니다.
- **상류 ARTEX 0.3.15 의 인증 하드닝을 이식했습니다.** 비밀번호 관련 설정 읽기가 실패하면 이제 `initialized:false` 로 포장하지 않고 503 으로 닫습니다(`authStatus`·`authInit`·`authLogin`·비밀번호 변경). 최초 비밀번호 설정은 `INSERT ... ON CONFLICT DO NOTHING`([`db.InsertSettingIfAbsent`](db/settings.go))으로 원자화해, 읽기 검사가 흔들려도 기본키 제약이 기존 값을 지킵니다. 72바이트를 넘는 비밀번호는 500 대신 400 으로 거부하고, 데이터베이스 커넥션 풀 상한(32)을 두어 `too many clients` 오류가 fail-open 판단을 흔드는 경로를 줄였습니다. 상류 커밋 `a951e4a`(0.3.15)에 맞춰 이식했으며, 이 포크에는 PR #20 과 겹치지 않는 부분만 더했습니다.

### 현지화 (i18n)

- **영어·중국어·스페인어 전환을 추가했습니다.** 기본 언어는 그대로 한국어이고, 설정 화면의 "표시 언어"에서 고르면 웹 UI(`web/messages/{ko,en,zh,es}.json`)와 에이전트가 쓰는 리포트·요약·대화 응답, 서버 오류 메시지·상태 라벨이 함께 바뀝니다. 운영자 콘솔 로그와 서버 기본값은 `ARTEX_LANG`(또는 설정 파일의 `language`)으로 정합니다. 에이전트의 행동 지침 본문은 이번에도 원문 그대로 둡니다.
- **사용자 노출 출력을 한국어로 강제했습니다.** 벤치마크된 에이전트의 행동 지침 본문(두뇌)은 성능 보존을 위해 원문 그대로 두고, 코드 고정 세그먼트(`langDirective`)로 사용자에게 보이는 산출물(취약점 리포트, 사실 요약, 최종 요약, 채팅 응답)만 한국어로 작성하도록 지시합니다. 명령·페이로드·코드·로그 원문은 원본을 보존합니다.
- **웹 UI 를 한국어로 옮겼습니다.** Next App Router 에 `next-intl` 을 도입하고 문자열을 `web/messages/ko.json` 과 `web/messages/zh.json` 으로 분리했습니다. 원본 중국어는 `zh.json` 에 보존해 상류 업데이트와 대조합니다. 대시보드·취약점·대화·알림 발송·가로채기·LLM 설정 등 화면 문자열을 한국어로 옮겼습니다.
- **서버 API 의 사용자 노출 오류·응답을 한국어로 옮겼습니다.** 브라우저로 돌아가는 HTTP 오류·응답 문구를 한국어로 교체했습니다. 단 에이전트 두뇌의 입력으로 되먹여지는 문구는 벤치마크 드리프트를 막기 위해 원문을 유지했고, 그 판정 근거는 저장소 작업 문서에 기록했습니다.
- **문서를 한국어로 정비했습니다.** 한국어 `README.md` 를 만들고 영어 `README.en.md` 를 함께 두었으며, 원본 중국어는 `README.zh.md` 로 보존했습니다.

### 방어·탐지 자료

- **방어·탐지 가이드를 추가했습니다.** 자율 AI 공격이 기존 스캐너와 무엇이 다른가, 방어자가 관측할 수 있는 지문(IoC), 진입점과 하드닝, 탐지 규칙, 사고 대응을 정리한 한국어 가이드([`docs/defense-ko.md`](docs/defense-ko.md))와 같은 내용의 영어판([`docs/defense-en.md`](docs/defense-en.md))을 두었습니다.
- **배포용 탐지 규칙을 제공합니다.** 가이드의 지문을 바로 쓸 수 있는 규칙으로 옮겼습니다. 호스트·로그 계층은 [Sigma](https://sigmahq.io) 원자·상관 규칙([`detections/sigma/`](detections/sigma/)), 네트워크 계층은 enrich 프로브와 norma SDK WebFetch 의 User-Agent 를 겨냥한 [Suricata](https://suricata.io) 규칙([`detections/suricata/`](detections/suricata/))으로 담았습니다.
- **ATT&CK 커버리지를 가시화했습니다.** 규칙이 태깅하는 기법을 MITRE ATT&CK Navigator 레이어([`detections/attack/`](detections/attack/))로 정리했습니다.
- **기계 판독 침해지표(IoC)를 표준 형식으로 제공합니다.** ARTEX 가 내보내는 고유 지문을 한 파일로 모은 CSV([`detections/indicators/artex_indicators.csv`](detections/indicators/artex_indicators.csv))와, 같은 지표를 위협 인텔리전스 플랫폼에 바로 가져올 수 있는 MISP 이벤트([`detections/indicators/artex_indicators.misp.json`](detections/indicators/artex_indicators.misp.json))로 담았습니다. 규칙이 받쳐 주는 지표는 `to_ids` 로, 호스트 포렌식 포트는 분류용 단서로 구분해 표기합니다.
- **재현 가능한 탐지 테스트를 붙였습니다.** 규칙을 실제로 돌려 증명하는 테스트 여덟 종(Sigma 구조·컴파일 검증, Sigma 실시간 이벤트 매칭, 백엔드 이식성, SigmaHQ 관례 린트, Suricata 로드·발화, ATT&CK 레이어 정합, 지표-소스 일치, MISP 내보내기 ↔ CSV 동기화)과 이를 한 번에 돌리는 일괄 러너·pre-commit 예시를 추가하고 CI 머지 게이트로 연결했습니다. Sigma 실시간 이벤트 매칭은 규칙이 컴파일될 뿐 아니라 악성 샘플 이벤트에는 실제로 발화하고 정상 이벤트에는 침묵하는지까지 원자·상관 규칙 모두에서 확인합니다.

### 저장소 정비

- **보안·오남용 경고와 국내법 고지를 넣었습니다.** README 최상단에 사용 범위, 정보통신망법·개인정보보호법 고지, 오남용 금지 경고를 추가했습니다.
- **한국어 UI 스크린샷으로 화면 미리 보기를 교체했습니다.**
- **메인테이너 런북과 기여 가이드를 정비했습니다.** 상류 동기화·번역 드리프트를 막기 위한 런북([`MAINTAINING.md`](MAINTAINING.md))과 탐지 규칙 기여 계약([`CONTRIBUTING.md`](CONTRIBUTING.md))을 두었습니다. 런북에는 릴리스 발행 파이프라인의 빌드 전제와, 태그 없이 로컬에서 그 전제를 검증하는 절차도 함께 정리했습니다.
- **푸시·PR 머지 게이트 CI 를 추가했습니다.** 상류 저장소는 태그 릴리스에서만 CI 가 돌았지만, 이 포크는 모든 푸시와 PR 에서 Go 빌드·정적 분석(`go vet`)·단위 테스트([`ci.yml`](.github/workflows/ci.yml)), 한국어 UI 정적 빌드([`web.yml`](.github/workflows/web.yml)), 문서의 저장소 내부 링크·이미지 참조 무결성([`docs.yml`](.github/workflows/docs.yml))을 돌려, 한국어화 과정에서 생긴 회귀를 머지 전에 잡습니다. 데이터베이스가 있어야 하는 통합 테스트는 패키지마다 격리된 PostgreSQL 서비스로 함께 검증합니다. 문서 링크 검사는 외부 네트워크에 의존하지 않는 결정론적 스크립트([`scripts/check-doc-links.py`](scripts/check-doc-links.py))로 돌려, 다국어 문서가 서로를 가리키는 많은 상대 링크와 화면 미리 보기 이미지가 깨진 채 머지되는 것을 막습니다. 문서 앵커(`#헤딩`) 링크도 GitHub 과 같은 slug 규칙으로 헤딩과 대조해, 헤딩 글자가 바뀌어 조용히 끊긴 목차·상호 참조 링크를 함께 잡습니다. 탐지 규칙 스위트는 위 '방어·탐지 자료' 절에서 설명한 머지 게이트가 담당합니다.
- **외부 링크 생존을 주기적으로 점검합니다.** 방어 가이드가 가리키는 사고 신고 창구·표준 참조 같은 외부 링크는 원격 서버 상태에 의존해 flaky 하므로 머지 게이트에서 빼고, 비차단 워크플로([`external-links`](.github/workflows/external-links.yml))가 매주 월요일과 수동 실행으로 브라우저 User-Agent·GET·리다이렉트 추적 점검([`scripts/check-external-links.py`](scripts/check-external-links.py))을 돌립니다. 호스트는 살아 있는데 확인 방법만 막힌 경우(봇 차단·속도 제한)와 우리가 고칠 수 없는 상류 상속 죽은 링크(allowlist)는 실패로 치지 않아, 우리 문서가 큐레이션한 외부 링크가 새로 깨질 때만 빨갛게 드러냅니다.
- **기여·거버넌스 인프라를 갖췄습니다.** 버그·기능·번역 이슈 템플릿([`.github/ISSUE_TEMPLATE/`](.github/ISSUE_TEMPLATE/))과 풀 리퀘스트 템플릿([`PULL_REQUEST_TEMPLATE.md`](.github/PULL_REQUEST_TEMPLATE.md)), 보안 취약점 신고 정책([`SECURITY.md`](SECURITY.md)), 행동 강령([`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md))을 두어, 외부 기여자가 이슈·PR·보안 신고를 일관된 양식으로 제출하도록 했습니다.
- **해외 기여자를 위한 영어 문서 레이어를 완성했습니다.** 이 저장소는 한국어가 주 언어이지만, 한국어를 읽지 못하는 기여자·보안 연구자·방어자가 같은 정보에 도달하도록 핵심 문서의 영어판을 함께 두었습니다. 영어 `README.en.md`·방어 가이드([`docs/defense-en.md`](docs/defense-en.md))에 더해, 변경 이력([`CHANGELOG.en.md`](CHANGELOG.en.md)), 보안 신고 정책([`SECURITY.en.md`](SECURITY.en.md)), 행동 강령([`CODE_OF_CONDUCT.en.md`](CODE_OF_CONDUCT.en.md)), 기여 가이드([`CONTRIBUTING.en.md`](CONTRIBUTING.en.md)), 메인테이너 런북([`MAINTAINING.en.md`](MAINTAINING.en.md)), 트래픽 증거 설계 문서([`docs/finding-traffic-evidence-en.md`](docs/finding-traffic-evidence-en.md)), 그리고 버그·기능·번역 이슈 템플릿의 영어판을 갖췄습니다. 한국어판과 영어판은 머리말에서 서로를 가리켜, 어느 언어로 들어와도 반대쪽으로 이동할 수 있습니다. (풀 리퀘스트 템플릿은 현재 한국어판만 제공합니다.)

---

상류 ARTEX 프로젝트의 버전별 릴리스 이력과 기여자 목록은 [`CHANGELOG.zh.md`](CHANGELOG.zh.md) 에서 원문 그대로 볼 수 있습니다.
