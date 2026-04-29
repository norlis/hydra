import { state, setState, nodesList, groupedProxies } from '../store/cluster';

export default function TabBar() {
  return (
    <div class="flex items-center gap-1 mb-6 border-b border-gray-800">
      <button
        onClick={() => setState('activeTab', 'nodes')}
        class="px-4 py-2.5 text-sm font-semibold border-b-2 -mb-px transition-colors flex items-center gap-2"
        classList={{
          'text-emerald-400 border-emerald-400': state.activeTab === 'nodes',
          'text-gray-500 border-transparent hover:text-gray-300': state.activeTab !== 'nodes',
        }}
      >
        Nodes
        <span class="bg-gray-800 text-gray-400 px-1.5 py-0.5 rounded text-[10px]">
          {nodesList().length}
        </span>
      </button>
      <button
        onClick={() => setState('activeTab', 'proxies')}
        class="px-4 py-2.5 text-sm font-semibold border-b-2 -mb-px transition-colors flex items-center gap-2"
        classList={{
          'text-emerald-400 border-emerald-400': state.activeTab === 'proxies',
          'text-gray-500 border-transparent hover:text-gray-300': state.activeTab !== 'proxies',
        }}
      >
        Proxies
        <span class="bg-gray-800 text-gray-400 px-1.5 py-0.5 rounded text-[10px]">
          {groupedProxies().reduce((acc, g) => acc + g.proxies.length, 0)}
        </span>
      </button>
    </div>
  );
}
