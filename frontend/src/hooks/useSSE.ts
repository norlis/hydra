import { onMount, onCleanup } from 'solid-js';
import { setState, upsertNode, removeNode } from '../store/cluster';
import type { Node } from '../types';

export function useSSE() {
  onMount(() => {
    const sse = new EventSource('/api/events');

    const handleUpsert = (e: MessageEvent) => {
      upsertNode(JSON.parse(e.data) as Node);
    };

    const handleLeave = (e: MessageEvent) => {
      const node = JSON.parse(e.data) as Node;
      removeNode(node.id);
    };

    sse.addEventListener('node.joined',  handleUpsert);
    sse.addEventListener('node.updated', handleUpsert);
    sse.addEventListener('node.left',    handleLeave);

    sse.onopen  = () => setState('connected', true);
    sse.onerror = () => setState('connected', false);

    onCleanup(() => sse.close());
  });
}
