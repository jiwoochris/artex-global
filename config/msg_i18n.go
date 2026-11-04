package config

// Startup (pre-DB-connection) operator-facing messages. The settings table is not
// readable at this point, so these are translated against the configured default
// language (Language(): ARTEX_LANG > config file language > DefaultLanguage), not
// the runtime ActiveLanguage. Identifiers an operator must act on (ARTEX_PG_DSN,
// database, dsn, host/user/dbname) stay verbatim. Keys are the English source
// strings; English and unknown keys return the key unchanged.
var cfgCatalog = map[string]map[string]string{
	"environment variable ARTEX_PG_DSN": {"es": "variable de entorno ARTEX_PG_DSN"},
	"config file %s (database.dsn)":     {"es": "archivo de configuración %s (database.dsn)"},
	"config file %s (database fields)":  {"es": "archivo de configuración %s (campos database)"},
	"database configuration not found: environment variable ARTEX_PG_DSN is not set, and config file %s has no database (dsn or host/user/dbname) configuration. Create a config file or set the environment variable, then try again": {"es": "no se encontró la configuración de la base de datos: la variable de entorno ARTEX_PG_DSN no está definida y el archivo de configuración %s tampoco tiene configuración database (dsn o host/user/dbname). Cree un archivo de configuración o defina la variable de entorno e inténtelo de nuevo"},
}

// logCatalog holds operator-facing console log templates (startup + runtime
// diagnostics). Keyed by the English source template; only the Spanish translation
// is stored. Format verbs are preserved in the same positional order so
// fmt.Printf/log.Printf arguments still line up.
var logCatalog = map[string]map[string]string{
	"[config] config file: %s": {"es": "[config] archivo de configuración: %s"},
	"[config] config file: %s (not found · using only environment variable ARTEX_PG_DSN)": {"es": "[config] archivo de configuración: %s (no encontrado · usando solo la variable de entorno ARTEX_PG_DSN)"},
	"[config] skill directory: %s": {"es": "[config] directorio de skills: %s"},
	"paused":                       {"es": "pausada"},
	"resumed":                      {"es": "reanudada"},
	"[auth] moved JWT key from %s to %s (out of the discoverable workspace)":                                                           {"es": "[auth] clave JWT movida de %s a %s (fuera del espacio de trabajo detectable)"},
	"[auth] wrote new JWT key to %s":                                                                                                   {"es": "[auth] nueva clave JWT escrita en %s"},
	"[auth] ignoring %s because it is shorter than %d characters; initialise with the setup token instead":                             {"es": "[auth] se ignora %s porque tiene menos de %d caracteres; inicialice con el token de configuración"},
	"[auth] ignoring %s because it exceeds %d bytes; initialise with the setup token instead":                                          {"es": "[auth] se ignora %s porque supera los %d bytes; inicialice con el token de configuración"},
	"[auth] could not read the password state; skipping first-run initialisation: %v":                                                  {"es": "[auth] no se pudo leer el estado de la contraseña; se omite la inicialización inicial: %v"},
	"[auth] failed to hash %s: %v":                                                                                                     {"es": "[auth] no se pudo generar el hash de %s: %v"},
	"[auth] failed to store %s: %v":                                                                                                    {"es": "[auth] no se pudo guardar %s: %v"},
	"[auth] initialised the admin password from environment variable %s (username ARTEX)":                                              {"es": "[auth] contraseña de administrador inicializada desde la variable de entorno %s (usuario ARTEX)"},
	"[auth] the admin password has not been set yet. The setup token is the value of environment variable %s (not printed to the log)": {"es": "[auth] aún no se ha establecido la contraseña de administrador. El token de configuración es el valor de la variable de entorno %s (no se muestra en el registro)"},
	"[auth] failed to generate the setup token: %v":                                                                                    {"es": "[auth] no se pudo generar el token de configuración: %v"},
	"[auth] the admin password has not been set yet. Setup token: %s":                                                                  {"es": "[auth] aún no se ha establecido la contraseña de administrador. Token de configuración: %s"},
	"[auth] enter the value above in the \"Setup token\" field on the first screen (/setup), or send it as setup_token in the POST /api/auth/init body. This token is never sent over the network and is only visible in this console log":                                        {"es": "[auth] introduzca el valor anterior en el campo \"Token de configuración\" de la primera pantalla (/setup) o envíelo como setup_token en el cuerpo de POST /api/auth/init. Este token nunca se transmite por la red y solo puede verse en este registro de consola"},
	"[auth] warning: the admin password has not been set yet and the server is bound to %q, so other hosts can connect. Initialisation still requires the setup token, but do not expose this port to external networks until initial setup is complete (default 127.0.0.1:8787)": {"es": "[auth] advertencia: aún no se ha establecido la contraseña de administrador y el servidor está enlazado a %q, por lo que otros hosts pueden conectarse. La inicialización sigue requiriendo el token de configuración, pero no exponga este puerto a redes externas hasta completar la configuración inicial (predeterminado 127.0.0.1:8787)"},
	"[auth] initial setup rejected: setup token mismatch (client=%s)":                                                              {"es": "[auth] configuración inicial rechazada: el token de configuración no coincide (client=%s)"},
	"[auth] admin password initialised (client=%s)":                                                                                {"es": "[auth] contraseña de administrador inicializada (client=%s)"},
	"[auth] password change rejected: current password mismatch (client=%s)":                                                       {"es": "[auth] cambio de contraseña rechazado: la contraseña actual no coincide (client=%s)"},
	"[auth] admin password changed; all existing sessions are invalidated (client=%s)":                                             {"es": "[auth] contraseña de administrador cambiada; todas las sesiones existentes quedan invalidadas (client=%s)"},
	"[auth] login failed (client=%s)":                                                                                              {"es": "[auth] inicio de sesión fallido (client=%s)"},
	"[notify] background notification delivery loop disabled via %s (test-only) — vulnerability IM notifications will not be sent": {"es": "[notify] el bucle de notificaciones en segundo plano se desactivó mediante %s (solo pruebas) — no se enviarán alertas IM de vulnerabilidades"},
	"[mcp] from %s, discovered and cached %d tool(s)":                                                                              {"es": "[mcp] desde %s, se descubrieron y almacenaron en caché %d herramienta(s)"},
	"[mcp] auto-discovery at startup: failed to read the list: %v":                                                                 {"es": "[mcp] descubrimiento automático al inicio: no se pudo leer la lista: %v"},
	"[mcp] auto-discovery of %s at startup failed: %v":                                                                             {"es": "[mcp] falló el descubrimiento automático de %s al inicio: %v"},
	"[tools] refreshed orchestration/platform tool schemas to code defaults (one-time)":                                            {"es": "[tools] esquemas de herramientas orchestration/platform actualizados a los valores predeterminados del código (una sola vez)"},
	"[reporter] pre-configured the \"%s\" agent + finding trigger":                                                                 {"es": "[reporter] agente \"%s\" + disparador de finding preconfigurados"},
	"[custom-tool] auto-detected python interpreter: %s":                                                                           {"es": "[custom-tool] intérprete de python detectado automáticamente: %s"},
	"[pg] database config source: %s":                                                                                              {"es": "[pg] origen de la configuración de la base de datos: %s"},
	"[mcp] browser MCP capture proxy connected: %s (CA %s)":                                                                        {"es": "[mcp] proxy de captura de browser MCP conectado: %s (CA %s)"},
	"[mcp] browser MCP capture proxy setting removed":                                                                              {"es": "[mcp] configuración del proxy de captura de browser MCP eliminada"},
}

// trCfg translates a startup/operator message to the configured default language
// (Language()) when that language is Spanish. English is the source language, so an
// English configuration (or an unknown key) returns the input unchanged. Format
// templates are translated the same way: pass the template through trCfg before
// fmt.Sprintf/fmt.Errorf.
func trCfg(key string) string {
	if m, ok := cfgCatalog[key]; ok {
		if v, ok2 := m[Language()]; ok2 && v != "" {
			return v
		}
	}
	return key
}

// T translates an operator-facing console log template to the configured default
// language (Language()) when that language is Spanish. English is the source
// language, so an English configuration (or an unknown key) returns the input
// unchanged. Exposed for cmd and server log.Printf call sites. Format verbs are
// preserved in order, so wrap the template only:
// log.Printf(config.T(template), args...).
func T(key string) string {
	if m, ok := logCatalog[key]; ok {
		if v, ok2 := m[Language()]; ok2 && v != "" {
			return v
		}
	}
	return key
}
