export interface NodeInterface {
  private_ip: string;
  public_ip?: string;
  service_port: number;
}

export interface Node {
  id: string;
  healthy: boolean;
  started_at?: string;
  last_seen?: string;
  interfaces?: NodeInterface[];
}

export interface Proxy {
  address: string;
  node_id: string;
}

export interface ProxyGroup {
  nodeId: string;
  proxies: Proxy[];
}

export interface TestState {
  status: 'testing' | 'success' | 'error';
  result: string;
}

export interface ServerInfo {
  version: string;
  hostname: string;
}
