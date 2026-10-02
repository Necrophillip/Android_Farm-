# Android Farm

> Orquestación de emuladores Android en Apple Silicon + Web UI de control.

**Android Farm** es un laboratorio para levantar, controlar y automatizar emuladores
Android nativos en macOS (arm64). Combina:

- **[`avdctl/`](avdctl/)** — un fork de [ForkbombEu/avdctl](https://github.com/ForkbombEu/avdctl) con parches para
  aprovechar **Metal / Hypervisor.framework** y la arquitectura ARM de Apple.
- **[`farmui/`](farmui/)** — una Web UI (Go + Server-Sent Events) con métricas,
  macros grabables, instalación de APKs por ADB y control por dispositivo.

---

## Tabla de contenidos

- [¿Qué es?](#qué-es)
- [Características](#características)
- [Requisitos](#requisitos)
- [Instalación rápida](#instalación-rápida)
- [Uso](#uso)
- [Macros](#macros)
- [Apps por ADB](#apps-por-adb)
- [Región y tráfico](#región-y-tráfico)
- [Arquitectura](#arquitectura)
- [API REST](#api-rest)
- [Parches a avdctl](#parches-a-avdctl)
- [Rendimiento](#rendimiento)
- [Visualización y consumo](#visualización-y-consumo)
- [Limitaciones conocidas](#limitaciones-conocidas)
- [Estructura del repo](#estructura-del-repo)
- [Licencia y créditos](#licencia-y-créditos)

---

## ¿Qué es?

avdctl gestiona AVDs (Android Virtual Devices), imágenes *golden* y clones. Android Farm
añade una capa de control visual y automatización pensada para **probar aplicaciones en
flotas de emuladores** en un Mac con Apple Silicon:

- Crea y destruye AVDs desde el navegador.
- Arranca/detiene emuladores con **GPU Metal** o render por software.
- Mide **CPU/RAM por emulador** en tiempo real (push por SSE, sin polling).
- **Graba macros** (taps, swipes, texto, teclas) y las ejecuta en bucle por dispositivo.
- **Instala APKs en caliente** con ADB (sin reconstruir imágenes).
- Perfil **Lite (AOSP)** para minimizar consumo de CPU y temperatura.

---

## Características

| Área | Detalle |
|---|---|
| **Dashboard** | Tarjetas por dispositivo con estado, CPU%, RAM, uptime y capturas en vivo. |
| **Tiempo real** | Snapshot completo empujado por **SSE** (`/api/events`); sin re-render animado. |
| **Metal** | Backend gráfico configurable; por defecto `host` (Metal/MoltenVK) en macOS. |
| **Lite mode** | Imagen AOSP `default`, 2 vCPU, 2 GB, 720p, sin Google Play Services. |
| **Macros** | Grabación sobre un lienzo (canvas), guardado, y ejecución en loop por tarjeta. |
| **Apps** | Biblioteca de APKs + instalar/abrir/desinstalar por ADB en cada dispositivo. |
| **Métricas** | CPU, RSS y uptime vía `ps`, con historial para sparklines. |
| **SSH** | avdctl soporta delegar comandos a un host remoto por SSH (heredado del upstream). |

---

## Requisitos

- **macOS en Apple Silicon** (arm64) — probado en M4.
- **Homebrew**.
- **Go 1.26+**.
- **Android SDK command-line tools** (`sdkmanager`, `avdmanager`, `adb`, `emulator`).
- **JDK 17+** (para `sdkmanager`/`avdmanager`).
- **QEMU** (solo `qemu-img`, para conversión de imágenes).

```sh
brew install go qemu openjdk@21
brew install --cask android-commandlinetools
```

---

## Instalación rápida

```sh
# 1. Variables de entorno del SDK
export ANDROID_SDK_ROOT=/opt/homebrew/share/android-commandlinetools
export ANDROID_AVD_HOME="$HOME/.android/avd"
export JAVA_HOME=/opt/homebrew/opt/openjdk@21/libexec/openjdk.jdk/Contents/Home
export PATH="$ANDROID_SDK_ROOT/cmdline-tools/latest/bin:$ANDROID_SDK_ROOT/platform-tools:$ANDROID_SDK_ROOT/emulator:$PATH"

# 2. Dependencias del SDK
yes | sdkmanager --licenses
sdkmanager "platform-tools" "emulator" "system-images;android-35;default;arm64-v8a"

# 3. Compilar avdctl (orquestador)
cd avdctl && go build -o bin/avdctl ./cmd/avdctl

# 4. Compilar farmui (Web UI)
cd ../farmui && go build -o bin/farmui .

# 5. Crear un AVD ligero y arrancar la UI
../avdctl/bin/avdctl init-base --name dev-a35 \
  --image "system-images;android-35;default;arm64-v8a" --device pixel_6
./bin/farmui --addr :8080
```

Abre **http://localhost:8080**.

---

## Uso

### Crear un AVD
Botón **＋ Nuevo AVD**. Con *Modo ligero* activado usa AOSP sin GMS y un perfil de bajo
consumo (2 vCPU · 2 GB · 720p).

### Arrancar / detener
Cada tarjeta expone **▶ Arrancar** / **■ Detener**. La selección de puerto es automática
(par, dentro del rango adb 5554–5586).

### Consola del dispositivo
Botón **⌨ Consola**: pantalla en vivo, teclas de hardware, envío de texto, shell ADB y el
panel **📦 Apps**.

### Rendimiento
Botón **⚙︎ Rendimiento**: elige el backend `-gpu` (`host` = Metal, `auto`, `angle_indirect`,
`swiftshader_indirect`) y el modo ventana.

---

## Macros

Flujo **grabar → visualizar → confirmar → ejecutar**:

1. Abre la consola de un dispositivo.
2. Pulsa **⏺ Grabar** y traza sobre el lienzo: los clics cortos son *tap* y los arrastres
   *swipe*. El texto y las teclas se capturan igual.
3. Cada paso se dibuja en el canvas y se añade a la lista.
4. **💾 Guardar macro…** → nombre + app objetivo.
5. En la tarjeta, el selector asigna una macro y **🧩 Ejecutar** la corre **en bucle**
   hasta volver a pulsar (**⏹ Detener**).

Las coordenadas se guardan **normalizadas (0..1)**, así que una macro grabada en 720p se
reproduce en cualquier resolución. Se persisten en `~/.androidfarm/macros/*.json`.

---

## Apps por ADB

Instalación en caliente sobre un emulador **en ejecución** (no requiere cocinar imágenes):

```
GET    /api/instances/{name}/packages              # apps de terceros instaladas
POST   /api/instances/{name}/apks/{id}/install     # adb install -r -t
POST   /api/instances/{name}/packages/{pkg}/launch # resolve-activity + am start
DELETE /api/instances/{name}/packages/{pkg}        # adb uninstall
```

Sube APKs desde **📦 Bake → sección APKs** (drag & drop, múltiples) y luego instálalos por
dispositivo desde su consola. Los APKs viven en `~/.androidfarm/apks/`.

---

## Región y tráfico

Para validar tu apk según la región (los 32 estados de México) y analizar su tráfico:

- **🌎 Región** (consola de cada dispositivo): localiza el contenedor a un estado —
  **GPS** (`adb emu geo fix`), **zona horaria** (`persist.sys.timezone`) y **locale `es-MX`**.
  Requiere `adb root` (la imagen *userdebug* del emulador lo permite).
- **Proxy fijo por dispositivo**: define un proxy (`host:puerto`) que **tú controles**, aplicado
  vía `settings put global http_proxy`. Es un proxy único para QA por región, **sin rotación**.
- **Libreta de proxies**: guarda tus proxies (self-hosted o de pago) con una etiqueta y asígnalos
  con un clic (💾 guardar · Usar · 🗑 borrar). Persistida en `~/.androidfarm/proxies.json`.
- **Métricas de red**: cada tarjeta muestra **↓/↑ bytes/s** (y el acumulado rx/tx), leídos de
  `/proc/net/dev` del invitado, útil para analizar patrones de tráfico por app/región.

Asignación persistida en `~/.androidfarm/net.json`.

> Cambiar la **IP de origen** requiere un proxy ubicado en esa región; el GPS/zona/locale sí se
> pueden fijar localmente. FarmUI **no** genera, descubre ni rota proxies: solo administra los que
> tú registres. ¿Dónde conseguir uno? Aloja el tuyo (Squid/3proxy/WireGuard en un VPS de la región)
> o contrata un proveedor de proxies para testing.

---

## Arquitectura

```
┌─────────────────────────────┐        SSE / REST        ┌──────────────────────┐
│  farmui (Go)                │◄────────────────────────►│  Navegador (SPA)     │
│  ├─ Service (lifecycle)     │                          │  glassmorphism       │
│  ├─ metrics sampler (ps)    │                          └──────────────────────┘
│  ├─ macroRunner (loop)      │
│  ├─ apkStore / bakeManager  │
│  └─ HTTP + embed(web/)      │
└──────────────┬──────────────┘
               │ importa la librería
               ▼
┌─────────────────────────────┐
│  avdctl (pkg/avdmanager)    │──── exec ──► emulator · adb · avdmanager · sdkmanager · qemu-img
│  directorios del SDK        │
└─────────────────────────────┘
```

`farmui` **no reimplementa** el motor: usa la librería pública `pkg/avdmanager` de avdctl
(vía `replace` local) y delega en el emulador oficial de Google.

---

## API REST

| Método | Ruta | Descripción |
|---|---|---|
| `GET` | `/api/system` | Info del host: OS, arch, ABI, GPU, RAM, CPUs, version del emulador, imágenes. |
| `GET` | `/api/events` | Stream SSE con el snapshot completo (AVDs, sistema, macros, jobs, APKs, goldens). |
| `GET` | `/api/avds` | Lista de AVDs + estado en ejecución + métricas. |
| `POST` | `/api/avds` | Crear AVD `{name, image, device, lite}`. |
| `DELETE` | `/api/avds/{name}` | Eliminar AVD. |
| `POST` | `/api/avds/{name}/start` | Arrancar `{port, windowed}`. |
| `POST` | `/api/avds/{name}/stop` | Detener. |
| `POST` | `/api/avds/{name}/optimize` | Aplicar perfil Lite + desactivar animaciones. |
| `GET` | `/api/instances/{name}/screenshot` | Captura PNG. |
| `POST` | `/api/instances/{name}/input` | `tap` / `swipe` / `text` / `key`. |
| `POST` | `/api/instances/{name}/shell` | Ejecutar comando ADB shell. |
| `GET`/`POST`/`DELETE` | `/api/macros[/{id}]` | Listar / guardar / borrar macros. |
| `POST` | `/api/instances/{name}/macro/start` | Ejecutar macro `{id, loop}`. |
| `POST` | `/api/instances/{name}/macro/stop` | Detener macro. |
| `GET` | `/api/instances/{name}/packages` | Apps de terceros. |
| `POST` | `/api/instances/{name}/apks/{id}/install` | Instalar APK. |
| `DELETE` | `/api/instances/{name}/packages/{pkg}` | Desinstalar. |
| `POST` | `/api/instances/{name}/packages/{pkg}/launch` | Abrir app. |
| `POST` | `/api/instances/{name}/region` | Localizar el dispositivo a un estado MX `{state}` (GPS + TZ + locale). |
| `POST` | `/api/instances/{name}/proxy` | Fijar/quitar proxy `{proxy}` (vacío = quitar). |
| `GET` | `/api/regions` | Catálogo de los 32 estados con capital, coordenadas y TZ. |
| `GET`/`POST`/`DELETE` | `/api/proxies[/{id}]` | Libreta de proxies del usuario (host:puerto). |
| `GET`/`POST`/`DELETE` | `/api/apks[/{id}]` | Biblioteca de APKs (multipart). |
| `GET`/`DELETE` | `/api/goldens[/{name}]` | Imágenes golden. |
| `POST` | `/api/bake` · `GET /api/jobs` | Bake de golden con APKs (ver limitaciones). |
| `GET`/`PUT` | `/api/settings` | GPU y modo ventana. |

---

## Parches a avdctl

Este fork añade mejoras específicas para Apple Silicon:

- **GPU configurable** (`Env.GPU`, `AVDCTL_GPU`): por defecto `host` en macOS → **Metal vía
  MoltenVK**. Antes estaba fijado a `swiftshader_indirect` (software).
- **Modo ventana** (`AVDCTL_WINDOW=1`) y **modo escribible headless** (`AVDCTL_WRITABLE=1`).
- **ABI nativa** (`HostABI`): detecta `arm64-v8a` en vez de asumir `x86_64`.
- **`findEmulatorPID` portable**: en macOS resolvía siempre `0` (solo leía `/proc`); ahora usa
  un escaneo cacheado de `ps`.
- **API pública extendida**: `avdmanager.Environment` expone `GPU`, `Windowed`, `Writable`, `ABI`.
- **Clones sparse-preserving**: `qemu-img convert` conserva huecos (~750 MB/clon en vez de 10 GB).
- **`persistDataPartition`**: normaliza `config.ini` al crear un AVD (elimina
  `disk.dataPartition.path=<temp>`). *Ver limitaciones.*

---

## Rendimiento

Medido en un M4 (16 GB), mismo hardware, tras el arranque:

| Imagen | vCPU | CPU ocioso (media) | Notas |
|---|---|---|---|
| `google_apis_playstore` | 4 | **~373 %** | Google Play Services + Play Store. Muy pesado. |
| `default` (AOSP) + Lite | 2 | **~20 %** | ~18× menos CPU. Recomendado para flotas. |

**Consejo:** usa siempre **Modo Lite** (AOSP `default`) para orquestar varios emuladores.
Reserva las imágenes con Google Play Services solo cuando la app los necesite.

---

## Visualización y consumo

Ver las pantallas en vivo es lo más caro de la granja: cada captura fuerza composición y
**encode PNG en el invitado**, transferencia ADB y **decode en el navegador**. Con muchas
instancias eso escala lineal y golpea CPU/GPU del host y del navegador. Por eso la
visualización está **separada de la orquestación**:

- **Plano de control (siempre activo):** métricas CPU/RAM, macros, shell, ADB y capturas
  bajo demanda. Cuestan ~nada.
- **Plano de visualización (opt-in y acotado):** streaming solo de lo que estás mirando.

Controles:

| Control | Qué hace |
|---|---|
| **📺 Vistas: ON/OFF** (header) | Enciende/apaga todas las vistas en vivo. Métricas y control siguen. |
| **👁 / 🚫** (por tarjeta) | Activa/desactiva la vista en vivo de ese dispositivo. |
| **📸** (tarjeta / consola) | Captura puntual `?force=1` cuando la vista está apagada. |
| **Scroll** | Solo se capturan las tarjetas visibles; al salir del viewport se pausan y reanudan al volver. |
| **Caché 1 s (backend)** | Varias pestañas/navegadores comparten una sola captura por dispositivo. |

Nota: esto elimina el sobrecosto de farmui, pero **no** apaga el render propio del emulador
(Metal/vsync) mientras esté corriendo — eso solo baja con menos resolución o menos emuladores.

---

## Limitaciones conocidas

- **Bake / golden con APKs**: en Android 35 (arm64, emulador 37.x) `/data` se reinicializa en
  cada *cold boot* (la persistencia del emulador depende del snapshot de QuickBoot). Por eso
  las apps **no sobreviven** al guardado en golden. **Usa el flujo de Apps por ADB** en su lugar.
- **Google Play Services**: la imagen `default` (AOSP) **no** trae GMS. Las apps que dependen de
  él pueden mostrar estado "sin conexión". Cambia a `system-images;android-35;google_apis;arm64-v8a`.
- **Geo-restricciones**: si una app está bloqueada en tu región, es respuesta del servidor, no
  del emulador (la red funciona: DNS, ICMP y HTTPS verificados).
- **iOS**: soportado por avdctl vía `xcrun simctl`, pero farmui se centra en Android.

---

## Estructura del repo

```
Android_Farm-/
├── avdctl/            # fork del orquestador (Go) — AGPL-3.0
│   ├── cmd/avdctl/    # CLI
│   ├── internal/      # avd · ios · redroid · sshclient
│   └── pkg/           # avdmanager · iosmanager · redroidmanager (API pública)
├── farmui/            # Web UI + API REST (Go, embed)
│   ├── main.go        # flags y arranque
│   ├── service.go     # lifecycle, métricas, apps, goldens
│   ├── macros.go      # grabación/ejecución de macros
│   ├── bake.go        # biblioteca de APKs, goldens, jobs
│   ├── metrics.go     # muestreo CPU/RAM
│   ├── server.go      # rutas HTTP + SSE
│   └── web/           # SPA (index.html · styles.css · app.js)
├── .gitignore
└── README.md
```

---

## Licencia y créditos

- **[`avdctl/`](avdctl/)** es un fork de [ForkbombEu/avdctl](https://github.com/ForkbombEu/avdctl),
  licenciado **AGPL-3.0-only** — Copyright (C) 2025 Forkbomb B.V. Los archivos conservan su
  cabecera original.
- **Android Farm / `farmui`** se distribuye bajo los términos de la **AGPL-3.0** por derivar de
  y enlazar con avdctl.
- Android, el emulador y las imágenes del sistema son propiedad de Google LLC.
