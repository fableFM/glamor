//! glamor Tauri shell (D-07): окно + трей + нотификации + управление
//! жизнью демона glamord (sidecar). Бизнес-логики в Rust НЕТ — только
//! lifecycle демона и проброс daemon.json/token во фронт.
//!
//! UNVERIFIED: написано без локальной сборки (нет Rust toolchain на машине
//! автора) — см. tasks/T-18-tauri-shell.md, секция «Блокер».

use serde::Serialize;
use std::sync::Mutex;
use std::time::Duration;
use tauri::{AppHandle, Manager, State, WindowEvent};
use tauri_plugin_notification::NotificationExt;

/// DaemonInfo — то, что фронт получает один раз при старте (invoke
/// `daemon_info`). Порт/токен из ~/.glamor (D-08); бизнес-данные дальше
/// идут только по HTTP/WS напрямую (D-07).
#[derive(Serialize, Clone)]
struct DaemonInfo {
    url: String,
    token: String,
}

/// DaemonState — наблюдаемое состояние демона для трея.
struct DaemonState {
    child: Option<tauri_plugin_shell::process::CommandChild>,
    restarts: u32,
    last_info: Option<DaemonInfo>,
}

const MAX_RESTARTS: u32 = 5;

#[tauri::command]
fn daemon_info(state: State<'_, Mutex<DaemonState>>) -> Result<DaemonInfo, String> {
    let st = state.lock().map_err(|e| e.to_string())?;
    st.last_info.clone().ok_or_else(|| "daemon not ready".to_string())
}

fn glamor_home() -> std::path::PathBuf {
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".to_string());
    std::path::Path::new(&home).join(".glamor")
}

/// Читает daemon.json + token (демон их пишет при старте, T-12).
fn read_daemon_info() -> Option<DaemonInfo> {
    let home = glamor_home();
    let daemon_json = std::fs::read_to_string(home.join("daemon.json")).ok()?;
    let parsed: serde_json::Value = serde_json::from_str(&daemon_json).ok()?;
    let url = parsed.get("url")?.as_str()?.to_string();
    let token = std::fs::read_to_string(home.join("token")).ok()?;
    Some(DaemonInfo {
        url,
        token: token.trim().to_string(),
    })
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            // второй запуск — фокус существующего окна (T-18)
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.unminimize();
                let _ = window.set_focus();
            }
        }))
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_window_state::Builder::new().build())
        .manage(Mutex::new(DaemonState {
            child: None,
            restarts: 0,
            last_info: None,
        }))
        .invoke_handler(tauri::generate_handler![daemon_info])
        .setup(|app| {
            setup_tray(app)?;
            start_daemon_watch(app.handle().clone());
            Ok(())
        })
        .on_window_event(|window, event| {
            // закрытие окна → сворачиваем в трей, демон продолжает работать
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .run(tauri::generate_context!())
        .expect("error while running glamor");
}

/// Трей: статус (polling демона), «Открыть», «Выйти».
fn setup_tray(app: &mut tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    use tauri::menu::{Menu, MenuItem};
    use tauri::tray::TrayIconBuilder;

    let open = MenuItem::with_id(app, "open", "Открыть glamor", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "Выйти", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open, &quit])?;

    let _tray = TrayIconBuilder::new()
        .menu(&menu)
        .tooltip("glamor")
        .on_menu_event(|app, event| match event.id.as_ref() {
            "open" => {
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.show();
                    let _ = window.set_focus();
                }
            }
            "quit" => {
                // по умолчанию демон НЕ останавливается (конфиг «quit also
                // stops daemon» = false); демон переживёт shell
                app.exit(0);
            }
            _ => {}
        })
        .build(app)?;
    Ok(())
}

/// Запуск и наблюдение за демоном: restart при крахе с backoff,
/// максимум MAX_RESTARTS, потом — экран ошибки (нотификация + лог).
fn start_daemon_watch(app: AppHandle) {
    std::thread::spawn(move || {
        // демон может уже работать (запущен внешне / предыдущий shell) —
        // тогда просто читаем daemon.json
        let mut backoff = Duration::from_secs(1);

        loop {
            if let Some(info) = read_daemon_info() {
                {
                    let state = app.state::<Mutex<DaemonState>>();
                    let mut st = state.lock().unwrap();
                    st.last_info = Some(info);
                }
                backoff = Duration::from_secs(1);
            } else {
                // демона нет — запускаем sidecar
                let state = app.state::<Mutex<DaemonState>>();
                let restarts = state.lock().unwrap().restarts;
                if restarts >= MAX_RESTARTS {
                    let _ = app
                        .notification()
                        .builder()
                        .title("glamor")
                        .body("glamord не смог стартовать 5 раз подряд — см. ~/.glamor/glamord.log")
                        .show();
                    break;
                }
                spawn_daemon(&app);
                state.lock().unwrap().restarts += 1;
            }

            std::thread::sleep(backoff);
            backoff = (backoff * 2).min(Duration::from_secs(60));
        }
    });
}

/// Старт sidecar-процесса glamord (TAURI_ENV_SIDEcar в build) или
/// внешнего бинаря в dev.
fn spawn_daemon(app: &AppHandle) {
    use tauri_plugin_shell::ShellExt;

    let sidecar = app.shell().sidecar("glamord");
    match sidecar {
        Ok(cmd) => match cmd.spawn() {
            Ok((_rx, child)) => {
                let state = app.state::<Mutex<DaemonState>>();
                state.lock().unwrap().child = Some(child);
            }
            Err(e) => {
                eprintln!("failed to spawn glamord sidecar: {e}");
            }
        },
        Err(e) => {
            eprintln!("glamord sidecar not configured: {e}");
        }
    }
}
