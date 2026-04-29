import { createSignal } from 'solid-js';
import { createStore, produce } from 'solid-js/store';
import type { Node, Proxy, TestState, ServerInfo } from '../types';

const [state, setState] = createStore({
  nodesMap: {} as Record<string, Node>,
  proxiesList: [] as Proxy[],
  serverInfo: null as ServerInfo | null,
  testStates: {} as Record<string, TestState>,
  activeTab: 'nodes' as 'nodes' | 'proxies',
  connected: false,
});

export { state, setState };

export const [now, setNow] = createSignal(Date.now());

export const nodesList = () =>
  Object.values(state.nodesMap).sort((a, b) => a.id.localeCompare(b.id));

export const groupedProxies = () => {
  const groups: Record<string, Proxy[]> = {};
  state.proxiesList.forEach(p => {
    groups[p.node_id] ??= [];
    groups[p.node_id].push(p);
  });
  return Object.keys(groups)
    .sort()
    .map(id => ({ nodeId: id, proxies: groups[id] }));
};

export const getPrimaryIP  = (n: Node) => n.interfaces?.[0]?.private_ip ?? 'N/A';
export const getPublicIP   = (n: Node) => n.interfaces?.[0]?.public_ip ?? null;
export const getAllPorts    = (n: Node) => {
  const ports = (n.interfaces ?? []).map(i => i.service_port).filter(p => p > 0);
  return ports.length ? ports.join(', ') : '—';
};

export const calculateUptime = (startedAt?: string) => {
  if (!startedAt) return '—';
  const diff = Math.floor((now() - new Date(startedAt).getTime()) / 1000);
  if (diff < 0) return '0s';
  const h = Math.floor(diff / 3600);
  const m = Math.floor((diff % 3600) / 60);
  const s = diff % 60;
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
};

export const formatLastSeen = (ts?: string) =>
  ts
    ? new Date(ts).toLocaleTimeString([], {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      })
    : '—';

let proxyTimeout: ReturnType<typeof setTimeout>;

export const scheduleProxyRefresh = () => {
  clearTimeout(proxyTimeout);
  proxyTimeout = setTimeout(async () => {
    try {
      const res = await fetch('/api/proxies');
      if (res.ok) setState('proxiesList', await res.json());
    } catch (e) {
      console.error('Failed to refresh proxies', e);
    }
  }, 300);
};

export const refreshAll = async () => {
  try {
    const [nRes, pRes, lRes] = await Promise.all([
      fetch('/api/nodes'),
      fetch('/api/proxies'),
      fetch('/live'),
    ]);
    if (nRes.ok) {
      const nodes: Node[] = await nRes.json();
      const map: Record<string, Node> = {};
      nodes.forEach(n => { map[n.id] = n; });
      setState('nodesMap', map);
    }
    if (pRes.ok) setState('proxiesList', await pRes.json());
    if (lRes.ok) setState('serverInfo', await lRes.json());
  } catch (e) {
    console.error('Initial load failed', e);
  }
};

export const testProxy = async (address: string) => {
  setState('testStates', address, { status: 'testing', result: '' });
  try {
    const res  = await fetch(`/api/proxies/test?address=${encodeURIComponent(address)}`);
    const data = await res.json();
    setState('testStates', address, {
      status: data.success ? 'success' : 'error',
      result: data.success ? data.ip : data.error,
    });
  } catch (e: unknown) {
    setState('testStates', address, {
      status: 'error',
      result: e instanceof Error ? e.message : 'Unknown error',
    });
  }
};

export const upsertNode = (node: Node) => {
  setState('nodesMap', node.id, node);
  scheduleProxyRefresh();
};

export const removeNode = (id: string) => {
  setState('nodesMap', produce(map => { delete map[id]; }));
  scheduleProxyRefresh();
};
