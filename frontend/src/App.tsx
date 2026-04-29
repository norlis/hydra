import { onMount, onCleanup, For, Show } from 'solid-js';
import { refreshAll, setNow, state, nodesList, groupedProxies } from './store/cluster';
import { useSSE } from './hooks/useSSE';
import Header from './components/Header';
import TabBar from './components/TabBar';
import NodeCard from './components/NodeCard';
import ProxyGroup from './components/ProxyGroup';

// Injected by the Go server via window.__APP_CONFIG__ (see index.html)
const clientId: string =
  (window as any).__APP_CONFIG__?.clientId ?? 'local-dev';

export default function App() {
  useSSE();

  onMount(async () => {
    await refreshAll();
    const timer = setInterval(() => setNow(Date.now()), 1000);
    onCleanup(() => clearInterval(timer));
  });

  return (
    <div class="container mx-auto max-w-7xl p-4 sm:p-6">
      <Header clientId={clientId} />

      <section class="bg-gray-900/30 border border-gray-800 rounded-xl p-5 backdrop-blur-sm">
        <TabBar />

        <Show when={state.activeTab === 'nodes'}>
          <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
            <For each={nodesList()}>
              {node => <NodeCard node={node} />}
            </For>
          </div>
        </Show>

        <Show when={state.activeTab === 'proxies'}>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <For each={groupedProxies()}>
              {group => <ProxyGroup group={group} />}
            </For>
          </div>
        </Show>
      </section>
    </div>
  );
}
