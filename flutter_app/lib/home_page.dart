import 'package:flutter/material.dart';
import 'proxy_service.dart';

class HomePage extends StatefulWidget {
  const HomePage({super.key});

  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> {
  final ProxyService _proxy = ProxyService();
  final TextEditingController _urlController = TextEditingController();

  @override
  void initState() {
    super.initState();
    _proxy.addListener(() => setState(() {}));
  }

  @override
  void dispose() {
    _proxy.dispose();
    _urlController.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Nanfang', style: TextStyle(fontWeight: FontWeight.bold)),
        centerTitle: true,
      ),
      body: Column(
        children: [
          // Subscription URL
          Padding(
            padding: const EdgeInsets.all(16),
            child: TextField(
              controller: _urlController,
              decoration: InputDecoration(
                labelText: 'Subscription URL',
                border: const OutlineInputBorder(),
                suffixIcon: IconButton(
                  icon: const Icon(Icons.download),
                  onPressed: () {
                    _proxy.setSubUrl(_urlController.text);
                    _proxy.fetchSubscription();
                  },
                ),
              ),
              onSubmitted: (v) {
                _proxy.setSubUrl(v);
                _proxy.fetchSubscription();
              },
            ),
          ),

          // Status bar
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
            color: _proxy.isRunning ? Colors.green.withOpacity(0.2) : Colors.grey.withOpacity(0.1),
            child: Row(
              children: [
                Icon(
                  _proxy.isRunning ? Icons.check_circle : Icons.stop_circle,
                  color: _proxy.isRunning ? Colors.green : Colors.grey,
                  size: 20,
                ),
                const SizedBox(width: 8),
                Expanded(child: Text(_proxy.status)),
                if (_proxy.isRunning)
                  TextButton(
                    onPressed: _proxy.stopProxy,
                    child: const Text('Stop', style: TextStyle(color: Colors.red)),
                  ),
              ],
            ),
          ),

          // Port setting
          if (!_proxy.isRunning)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: Row(
                children: [
                  const Text('SOCKS5 Port:'),
                  const SizedBox(width: 8),
                  SizedBox(
                    width: 80,
                    child: TextField(
                      keyboardType: TextInputType.number,
                      decoration: const InputDecoration(
                        border: OutlineInputBorder(),
                        contentPadding: EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                      ),
                      controller: TextEditingController(text: '${_proxy.listenPort}'),
                      onSubmitted: (v) {
                        final port = int.tryParse(v);
                        if (port != null) _proxy.setListenPort(port);
                      },
                    ),
                  ),
                ],
              ),
            ),

          const SizedBox(height: 8),

          // Node list
          Expanded(
            child: _proxy.nodes.isEmpty
                ? const Center(child: Text('No nodes loaded'))
                : ListView.builder(
                    itemCount: _proxy.nodes.length,
                    itemBuilder: (context, index) {
                      final node = _proxy.nodes[index];
                      final selected = index == _proxy.selectedNodeIndex;
                      return ListTile(
                        leading: CircleAvatar(
                          backgroundColor: selected ? Colors.blue : Colors.grey,
                          child: Text('${node.nodeId}', style: const TextStyle(fontSize: 12)),
                        ),
                        title: Text(node.name),
                        subtitle: Text('${node.server}:${node.serverPort}'),
                        trailing: selected ? const Icon(Icons.check, color: Colors.blue) : null,
                        selected: selected,
                        onTap: () => _proxy.selectNode(index),
                      );
                    },
                  ),
          ),

          // Start button
          Padding(
            padding: const EdgeInsets.all(16),
            child: SizedBox(
              width: double.infinity,
              height: 48,
              child: ElevatedButton(
                onPressed: _proxy.nodes.isEmpty
                    ? null
                    : (_proxy.isRunning ? null : _proxy.startProxy),
                style: ElevatedButton.styleFrom(
                  backgroundColor: _proxy.isRunning ? Colors.grey : Colors.blue,
                ),
                child: Text(
                  _proxy.isRunning ? 'Running on :${_proxy.listenPort}' : 'Start Proxy',
                  style: const TextStyle(fontSize: 16, color: Colors.white),
                ),
              ),
            ),
          ),

          // Proxy info
          if (_proxy.isRunning)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
              child: Container(
                padding: const EdgeInsets.all(12),
                decoration: BoxDecoration(
                  color: Colors.blue.withOpacity(0.1),
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('SOCKS5: 127.0.0.1:${_proxy.listenPort}',
                        style: const TextStyle(fontFamily: 'monospace')),
                    const SizedBox(height: 4),
                    Text('Node: ${_proxy.nodes[_proxy.selectedNodeIndex].name}',
                        style: const TextStyle(color: Colors.grey)),
                  ],
                ),
              ),
            ),
        ],
      ),
    );
  }
}
