import { For, Show } from 'solid-js';
import type { ProxyGroup as ProxyGroupType } from '../types';
import { state, testProxy } from '../store/cluster';

interface Props {
  group: ProxyGroupType;
}

export default function ProxyGroup(props: Props) {
  return (
    <div class="bg-gray-800/30 border border-gray-700/50 rounded-xl p-5 shadow-lg">
      <h3 class="text-emerald-400 font-bold text-sm mb-4 flex items-center gap-2 border-b border-gray-800 pb-2">
        NODE: <span class="text-gray-200">{props.group.nodeId}</span>
      </h3>
      <div class="space-y-3">
        <For each={props.group.proxies}>
          {proxy => {
            const testState = () => state.testStates[proxy.address];
            const isTesting = () => testState()?.status === 'testing';

            return (
              <div class="bg-gray-900/50 border border-gray-700 rounded-lg p-3">
                <div class="flex justify-between items-center">
                  <span class="font-mono text-emerald-300 text-sm">{proxy.address}</span>
                  <button
                    onClick={() => testProxy(proxy.address)}
                    disabled={isTesting()}
                    class="px-3 py-1.5 rounded text-[10px] font-bold transition-all"
                    classList={{
                      'bg-gray-700': isTesting(),
                      'bg-emerald-600 hover:bg-emerald-500 text-white': !isTesting(),
                    }}
                  >
                    {isTesting() ? 'Testing...' : 'Test Tunnel'}
                  </button>
                </div>

                <Show when={testState()}>
                  <div class="mt-2 pt-2 border-t border-gray-800 text-[10px] font-mono">
                    <Show when={testState()?.status === 'success'}>
                      <div class="text-emerald-400">
                        ✓ Connected! IP via Proxy:{' '}
                        <span class="bg-emerald-900/40 px-1 rounded">
                          {testState()?.result}
                        </span>
                      </div>
                    </Show>
                    <Show when={testState()?.status === 'error'}>
                      <div class="text-red-400">
                        ✗ Failed: <span>{testState()?.result}</span>
                      </div>
                    </Show>
                  </div>
                </Show>
              </div>
            );
          }}
        </For>
      </div>
    </div>
  );
}
