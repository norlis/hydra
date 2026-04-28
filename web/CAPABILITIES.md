# Hydra Web UI — Capacidades actuales y propuestas

## Estado actual

### Rutas expuestas

| Ruta | Tipo | Descripción |
|------|------|-------------|
| `GET /events` | HTML | Página principal del dashboard |
| `GET /assets/*` | Static | CSS compilado (Tailwind), fuentes Fira Code |
| `GET /api/nodes` | JSON | Lista de nodos activos (local + peers) |
| `GET /api/proxies` | JSON | Lista de proxies con dirección primaria |
| `GET /api/events` | SSE | Stream en tiempo real de eventos del clúster |
| `GET /health` | JSON | Health check del nodo |
| `GET /debug/pprof/*` | HTML | Profiling Go (CPU, heap, goroutines, trace) |

---

### Vista de nodos (`/events`)

**Stack:** HTMX 2 + Alpine.js + Tailwind CSS (dark mode). Todo compilado como assets embebidos en el binario (`embed.FS`).

**Lo que hace hoy:**

- **Snapshot inicial**: al cargar la página hace `fetch /api/nodes` para poblar el estado existente sin esperar el primer evento SSE.
- **Live updates via SSE**: HTMX se suscribe a `/api/events` y puente hacia Alpine.js usando `htmx:sseMessage`. Maneja tres eventos: `node.joined`, `node.updated`, `node.left`.
- **Store reactivo**: Alpine mantiene un `nodesMap` (diccionario `id → node`) para hacer upserts/deletes en O(1) sin re-renderizar toda la lista.
- **Indicador LIVE**: badge pulsante que confirma que la conexión SSE está activa.
- **Card por nodo** muestra: `node.id`, estado healthy/unhealthy (franja lateral verde/roja), IP primaria + puerto, timestamp `last_seen`, cantidad de interfaces.
- **Ordenamiento estable**: la lista se ordena por `node.id` al derivar del mapa.
- **Empty state**: mensaje de espera cuando no hay nodos.
- **Client ID**: UUID único por sesión visible en el header (útil para debugging).

**Datos disponibles en `topology.Node` que la UI aún no muestra:**
- `zone` — zona geográfica / datacenter
- `labels` — metadatos arbitrarios (`provider`, `role`, etc.)
- `interfaces[n].name` — nombre de interfaz (eth0, en0)
- `interfaces[n].mac` — dirección MAC
- `interfaces[n].public_ip` — IP pública
- `interfaces[n].subnet_cidr` — subred
- `interfaces[n].reachable` — si la interfaz es alcanzable

---

## Capacidades propuestas

### Prioridad alta (funcionalidad faltante clara)

#### 1. Panel de interfaces por nodo
Expandir el card con un acordeón o modal que muestre todas las interfaces del nodo: nombre, IP privada, IP pública, CIDR, MAC, puerto y estado `reachable`. Hoy solo se muestra la interfaz `[0]`.

#### 2. Indicador de reconexión SSE
Si la conexión SSE cae (error de red, servidor reiniciado), HTMX reintenta pero la UI no lo refleja. Añadir un estado "Reconnecting..." que cambie el badge LIVE a naranja/rojo durante la reconexión.

#### 3. Vista de proxies
Aprovechar `GET /api/proxies` para mostrar una segunda pestaña con la lista de proxies activos y su dirección `node_id → ip:port`. Útil para operadores que necesitan saber a qué proxy apuntar clientes.

#### 4. Log de eventos en tiempo real
Un feed cronológico de los eventos SSE recibidos (`node.joined`, `node.left`, `node.updated`) con timestamp, tipo (color-coded) y datos del nodo afectado. Ayuda a diagnosticar flapping o reconexiones.

---

### Prioridad media (observabilidad operacional)

#### 5. Métricas de nodo (cuando OTEL esté disponible)
Panel con métricas básicas expuestas vía el MeterProvider existente: latencia de setup de conexión, duración de túneles, goroutines activas. Requiere un endpoint `/api/metrics/summary` o fetch al Collector.

#### 6. Mapa de topología (grafo)
Visualización de la consistent hash ring con D3.js o similar: nodos como círculos, aristas representando vecinos en el anillo. Útil para entender distribución de carga y detectar nodos aislados.

#### 7. Labels y zona visibles en el card
Mostrar `node.zone` como badge en el header del card y renderizar los `labels` como tags coloreados (`role=edge-proxy`, `provider=aws`). Los datos ya existen en la respuesta JSON.

---

### Prioridad baja (nice-to-have)

#### 8. Filtrado y búsqueda
Input de búsqueda para filtrar nodos por `id`, `zone` o `label`. Útil en clústeres con muchos nodos.

#### 9. Exportar snapshot
Botón que descargue el estado actual del clúster como JSON (el mismo payload de `/api/nodes`).

#### 10. Modo claro
Toggle de tema. Tailwind ya tiene soporte `dark:` en el CSS compilado; solo falta el toggle en Alpine y persistirlo en `localStorage`.

#### 11. Autenticación básica
El dashboard hoy es público para cualquiera que acceda al puerto de control. Un middleware de Basic Auth o token Bearer protegería el acceso en despliegues expuestos.

---

## Notas técnicas

- Los assets están embebidos en el binario con `//go:embed "assets"` — cualquier cambio requiere recompilar.
- El CSS usa Tailwind compilado (`output.css`); `global.css` es la fuente de entrada. Para agregar clases nuevas hay que recompilar con `tailwindcss`.
- HTMX está cargado desde CDN (`cdn.jsdelivr.net`); en entornos air-gapped conviene bundlear o auto-hospedar.