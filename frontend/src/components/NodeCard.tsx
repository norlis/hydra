import { Show } from 'solid-js';
import type { Node } from '../types';
import {
  getPrimaryIP,
  getPublicIP,
  getAllPorts,
  calculateUptime,
  formatLastSeen,
} from '../store/cluster';

interface Props {
  node: Node;
}

export default function NodeCard(props: Props) {
  return (
    <div
      class="node-card relative rounded-xl p-4 transition-all duration-300"
      classList={{ unhealthy: !props.node.healthy }}
    >
      <div class="flex items-center gap-2 mb-2 text-[10px] font-bold uppercase">
        <div
          class="w-1.5 h-1.5 rounded-full"
          classList={{
            'bg-emerald-400': props.node.healthy,
            'bg-red-400': !props.node.healthy,
          }}
        />
        <span
          classList={{
            'text-emerald-400': props.node.healthy,
            'text-red-400': !props.node.healthy,
          }}
        >
          {props.node.healthy ? 'Healthy' : 'Unhealthy'}
        </span>
      </div>

      <h3 class="text-lg font-bold text-gray-100 mb-3 truncate">{props.node.id}</h3>

      <div class="space-y-1 text-[11px] text-gray-400">
        <div>
          Private IP: <span class="text-gray-200">{getPrimaryIP(props.node)}</span>
        </div>
        <Show when={getPublicIP(props.node)}>
          <div>
            Public IP:{' '}
            <span class="text-emerald-500 font-bold">{getPublicIP(props.node)}</span>
          </div>
        </Show>
        <div>
          Ports: <span class="text-gray-200">{getAllPorts(props.node)}</span>
        </div>

        <div class="pt-2 text-emerald-400/80 font-bold">
          Uptime:{' '}
          <span class="text-emerald-400">{calculateUptime(props.node.started_at)}</span>
        </div>

        <div class="text-gray-500 italic">
          Last Seen: <span>{formatLastSeen(props.node.last_seen)}</span>
        </div>
      </div>
    </div>
  );
}
