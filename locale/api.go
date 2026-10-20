package locale

func init() {
	Register("language must be en or ko", "language must be en or ko", "언어는 en 또는 ko여야 합니다")
	Register("task not found", "task not found", "작업을 찾을 수 없습니다")
	Register("no active task", "no active task", "활성 작업이 없습니다")
}

func init() {
	Register("Unauthorized", "Unauthorized", "인증이 필요합니다")
	Register("Invalid or expired token", "Invalid or expired token", "토큰이 잘못되었거나 만료되었습니다")
	Register("Password is already set", "Password is already set", "암호가 이미 설정되었습니다")
	Register("Password cannot be empty", "Password cannot be empty", "암호는 비워 둘 수 없습니다")
	Register("Password hashing failed", "Password hashing failed", "암호 해시 생성 실패")
	Register("Save failed: ", "Save failed: ", "저장 실패: ")
	Register("Token generation failed", "Token generation failed", "토큰 생성 실패")
	Register("Invalid request format", "Invalid request format", "요청 형식이 잘못되었습니다")
	Register("New password cannot be empty", "New password cannot be empty", "새 암호는 비워 둘 수 없습니다")
	Register("Password is not initialized; set it first", "Password is not initialized; set it first", "암호가 초기화되지 않았습니다. 먼저 설정하세요")
	Register("Current password is incorrect", "Current password is incorrect", "현재 암호가 잘못되었습니다")
	Register("Incorrect username or password", "Incorrect username or password", "사용자 이름 또는 암호가 잘못되었습니다")
	Register("[auth] JWT key migrated from %s to %s (outside the browsable workspace)", "[auth] JWT key migrated from %s to %s (outside the browsable workspace)", "[auth] JWT 키를 %s에서 %s(탐색 가능한 작업 공간 밖)(으)로 이전했습니다")
	Register("[auth] New JWT key written to %s", "[auth] New JWT key written to %s", "[auth] 새 JWT 키를 %s에 저장했습니다")
	Register("Password must contain at least 8 characters", "Password must contain at least 8 characters", "암호는 최소 8자 이상이어야 합니다")
	Register("Password must not exceed 72 bytes", "Password must not exceed 72 bytes", "암호는 72바이트를 초과할 수 없습니다")
	Register("Invalid setup token. Enter the token printed on the [auth] line of the server console log", "Invalid setup token. Enter the token printed on the [auth] line of the server console log", "설정 토큰이 잘못되었습니다. 서버 콘솔 로그의 [auth] 줄에 출력된 토큰을 입력하세요")
	Register("Too many failed attempts; please try again later", "Too many failed attempts; please try again later", "인증 실패 횟수가 너무 많습니다. 잠시 후 다시 시도하세요")
	Register("[auth] %s is shorter than %d characters and is ignored. Initialize with the setup token instead", "[auth] %s is shorter than %d characters and is ignored. Initialize with the setup token instead", "[auth] %s(은)는 %d자보다 짧아 무시됩니다. 대신 설정 토큰으로 초기화하세요")
	Register("[auth] %s exceeds %d bytes and is ignored. Initialize with the setup token instead", "[auth] %s exceeds %d bytes and is ignored. Initialize with the setup token instead", "[auth] %s(은)는 %d바이트를 초과하여 무시됩니다. 대신 설정 토큰으로 초기화하세요")
	Register("[auth] Failed to hash %s: %v", "[auth] Failed to hash %s: %v", "[auth] %s(으)해시 생성 실패: %v")
	Register("[auth] Failed to store %s: %v", "[auth] Failed to store %s: %v", "[auth] %s(을)저장하지 못했습니다: %v")
	Register("[auth] Initialized the admin password from %s (username ARTEX)", "[auth] Initialized the admin password from %s (username ARTEX)", "[auth] %s에서 관리자 암호를 초기화했습니다(사용자 이름 ARTEX)")
	Register("[auth] Skipping first-run initialization because the password state could not be read: %v", "[auth] Skipping first-run initialization because the password state could not be read: %v", "[auth] 암호 상태를 읽을 수 없어 최초 실행 초기화를 건너뜁니다: %v")
	Register("[auth] The admin password is not set yet. The setup token is the %s environment variable value (never logged)", "[auth] The admin password is not set yet. The setup token is the %s environment variable value (never logged)", "[auth] 관리자 암호가 아직 설정되지 않았습니다. 설정 토큰은 %s 환경 변수 값이며(로그에 남지 않음) 그 값을 사용합니다")
	Register("[auth] Failed to generate a setup token: %v", "[auth] Failed to generate a setup token: %v", "[auth] 설정 토큰 생성에 실패했습니다: %v")
	Register("[auth] The admin password is not set yet. Setup token: %s", "[auth] The admin password is not set yet. Setup token: %s", "[auth] 관리자 암호가 아직 설정되지 않았습니다. 설정 토큰: %s")
	Register("[auth] Enter the value above in the \"setup token\" field on the first screen (/setup), or send it as setup_token in the POST /api/auth/init body. No API response ever returns this token; the only copy outside this console log is the one you submit yourself", "[auth] Enter the value above in the \"setup token\" field on the first screen (/setup), or send it as setup_token in the POST /api/auth/init body. No API response ever returns this token; the only copy outside this console log is the one you submit yourself", "[auth] 위 값을 첫 화면(/setup)의 \"설정 토큰\" 입력란에 넣거나, POST /api/auth/init 본문에 setup_token으로 전송하세요. 어떤 API 응답도 이 토큰을 반환하지 않으며, 이 콘솔 로그 외에 존재하는 사본은 직접 제출한 것뿐입니다")
	Register("[auth] First-run setup rejected: setup token mismatch (client=%s)", "[auth] First-run setup rejected: setup token mismatch (client=%s)", "[auth] 최초 실행 설정을 거부했습니다: 설정 토큰 불일치(클라이언트=%s)")
	Register("[auth] Admin password initialized (client=%s)", "[auth] Admin password initialized (client=%s)", "[auth] 관리자 암호가 초기화되었습니다(클라이언트=%s)")
	Register("[auth] Login failed (client=%s)", "[auth] Login failed (client=%s)", "[auth] 로그인에 실패했습니다(클라이언트=%s)")
	Register("[auth] Password change rejected: current password mismatch (client=%s)", "[auth] Password change rejected: current password mismatch (client=%s)", "[auth] 암호 변경을 거부했습니다: 현재 암호 불일치(클라이언트=%s)")
	Register("[auth] Admin password changed; all existing sessions are now invalid (client=%s)", "[auth] Admin password changed; all existing sessions are now invalid (client=%s)", "[auth] 관리자 암호가 변경되어 모든 기존 세션이 무효가되었습니다(클라이언트=%s)")
	Register("[auth] Warning: the admin password is not set yet but the server is bound to %q and reachable from other hosts. Initialization is impossible without the setup token, but do not expose this port to external networks until setup is done (default 127.0.0.1:8787)", "[auth] Warning: the admin password is not set yet but the server is bound to %q and reachable from other hosts. Initialization is impossible without the setup token, but do not expose this port to external networks until setup is done (default 127.0.0.1:8787)", "[auth] 경고: 관리자 암호가 아직 설정되지 않았지만 서버가 %q에 바인딩되어 다른 호스트에서 접근할 수 있습니다. 설정 토큰 없이는 초기화할 수 없으므로, 설정이 완료될 때까지 이 포트를 외부 네트워크에 노출하지 마세요(기본값 127.0.0.1:8787)")
}

func init() {
	Register("Preparing…", "Preparing…", "준비 중…")
	Register("Update failed", "Update failed", "업데이트 실패")
	Register("New version ready; restarting…", "New version ready; restarting…", "새 버전 준비 완료. 재시작 중…")
	Register("Current version %q is not a stable release; one-click updates are disabled", "Current version %q is not a stable release; one-click updates are disabled", "현재 버전 %q은(는) 정식 릴리스가 아니므로 원클릭 업데이트가 비활성화되었습니다")
	Register("Already running the latest version %s", "Already running the latest version %s", "이미 최신 버전 %s을(를) 실행 중입니다")
	Register("An update is already in progress", "An update is already in progress", "업데이트가 이미 진행 중입니다")
	Register("[update] Update failed: %v", "[update] Update failed: %v", "[update] 업데이트 실패: %v")
	Register("[update] %s -> %s staged; exiting to complete replacement", "[update] %s -> %s staged; exiting to complete replacement", "[update] %s -> %s 준비 완료. 교체를 완료하기 위해 종료합니다")
	Register("Cannot roll back while an update is in progress", "Cannot roll back while an update is in progress", "업데이트 진행 중에는 롤백할 수 없습니다")
	Register("[update] Manually rolled back to the previous version; exiting to complete the switch", "[update] Manually rolled back to the previous version; exiting to complete the switch", "[update] 이전 버전으로 수동 롤백했습니다. 전환을 완료하기 위해 종료합니다")
}

func init() {
	Register("Data source is temporarily unavailable; please try again later", "Data source is temporarily unavailable; please try again later", "데이터 소스를 일시적으로 사용할 수 없습니다. 잠시 후 다시 시도하세요")
	Register("Password must contain at least %d characters", "Password must contain at least %d characters", "암호는 최소 %d자여야 합니다")
	Register("Password must not exceed %d bytes", "Password must not exceed %d bytes", "암호는 %d바이트를 초과할 수 없습니다")
}
