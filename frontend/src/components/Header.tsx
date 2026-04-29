import { Show } from 'solid-js';
import { state } from '../store/cluster';

interface Props {
  clientId: string;
}

export default function Header(props: Props) {
  return (
    <header class="flex flex-col md:flex-row md:items-center justify-between gap-4 mb-6 px-5 py-3 rounded-xl bg-gray-900/40 border border-gray-800 backdrop-blur-sm">
      <div class="flex items-center gap-3">
        <h1 class="text-xl font-bold tracking-wide text-emerald-400">Hydra Mesh</h1>
        <div class="w-px h-6 bg-gray-700" />
        <div class="flex items-center gap-2 px-2.5 py-1 rounded-full bg-emerald-500/10 border border-emerald-500/30 text-emerald-400 text-[10px] font-bold tracking-wider">
          <div class="w-1.5 h-1.5 rounded-full bg-emerald-500 live-dot" />
          LIVE TOPOLOGY
        </div>
      </div>

      <div class="flex items-center gap-3 overflow-x-auto pb-1 md:pb-0">
        <Show when={state.serverInfo}>
          {info => (
            <div class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-gray-900/60 border border-gray-800 text-xs text-gray-400 whitespace-nowrap">
              <span class="text-emerald-500 font-bold">v{info().version}</span>
              <span class="text-gray-700">|</span>
              <span class="truncate max-w-[150px]" title="Hostname">
                {info().hostname}
              </span>
            </div>
          )}
        </Show>

        <div class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-gray-900/60 border border-gray-800 text-xs text-gray-400 whitespace-nowrap">
          <span>Client:</span>{' '}
          <span class="text-gray-200 font-bold">{props.clientId}</span>
        </div>
      </div>
    </header>
  );
}
