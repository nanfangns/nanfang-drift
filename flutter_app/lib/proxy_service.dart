import 'dart:convert';
import 'dart:io';
import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;
import 'package:path_provider/path_provider.dart';
import 'package:path/path.dart' as p;

class NodeInfo {
  final int nodeId;
  final String name;
  final String server;
  final int serverPort;
  final String password;
  final String edgePsk;
  final String aeadKey;

  NodeInfo({
    required this.nodeId,
    required this.name,
    required this.server,
    required this.serverPort,
    required this.password,
    required this.edgePsk,
    required this.aeadKey,
  });

  factory NodeInfo.fromJson(Map<String, dynamic> json) {
    return NodeInfo(
      nodeId: json['node_id'] ?? 0,
      name: json['name'] ?? '',
      server: json['server'] ?? '',
      serverPort: json['server_port'] ?? 0,
      password: json['password'] ?? '',
      edgePsk: json['aero_v2_edge_psk'] ?? '',
      aeadKey: json['aero_v2_aead_key'] ?? '',
    );
  }
}

class ProxyService extends ChangeNotifier {
  List<NodeInfo> _nodes = [];
  bool _isRunning = false;
  Process? _process;
  String _status = 'stopped';
  String _subUrl = '';
  int _listenPort = 1080;
  int _selectedNodeIndex = 0;

  List<NodeInfo> get nodes => _nodes;
  bool get isRunning => _isRunning;
  String get status => _status;
  String get subUrl => _subUrl;
  int get listenPort => _listenPort;
  int get selectedNodeIndex => _selectedNodeIndex;

  void setSubUrl(String url) {
    _subUrl = url;
    notifyListeners();
  }

  void setListenPort(int port) {
    _listenPort = port;
    notifyListeners();
  }

  void selectNode(int index) {
    _selectedNodeIndex = index;
    notifyListeners();
  }

  Future<void> fetchSubscription() async {
    if (_subUrl.isEmpty) return;

    _status = 'fetching...';
    notifyListeners();

    try {
      final client = http.Client();
      final response = await client.get(
        Uri.parse(_subUrl),
        headers: {'User-Agent': 'nanfang/1.0'},
      ).timeout(const Duration(seconds: 15));
      client.close();

      if (response.statusCode == 200) {
        final List<dynamic> raw = jsonDecode(response.body);
        _nodes = raw
            .where((n) => n['type'] == 'aero_v2')
            .map((n) => NodeInfo.fromJson(n))
            .toList();
        _status = '${_nodes.length} nodes loaded';
      } else {
        _status = 'HTTP ${response.statusCode}';
      }
    } catch (e) {
      _status = 'Error: $e';
    }
    notifyListeners();
  }

  Future<String> _getCorePath() async {
    // Check if nanfang-core.exe exists next to the app
    final exeDir = p.dirname(Platform.resolvedExecutable);
    final corePath = p.join(exeDir, 'nanfang-core.exe');
    if (await File(corePath).exists()) return corePath;

    // Check in working directory
    final cwdCore = 'nanfang-core.exe';
    if (await File(cwdCore).exists()) return cwdCore;

    throw Exception(
      'nanfang-core.exe not found. Put nanfang-core.exe next to the app executable before starting the proxy.',
    );
  }

  Future<void> startProxy() async {
    if (_nodes.isEmpty || _isRunning) return;

    _status = 'starting...';
    notifyListeners();

    try {
      // Save nodes to temp file
      final tempDir = await getTemporaryDirectory();
      final nodesFile = p.join(tempDir.path, 'nanfang_nodes.json');
      final jsonStr = jsonEncode(_nodes.map((n) => {
        'node_id': n.nodeId,
        'name': n.name,
        'server': n.server,
        'server_port': n.serverPort,
        'password': n.password,
        'aero_v2_edge_psk': n.edgePsk,
        'aero_v2_aead_key': n.aeadKey,
      }).toList());
      await File(nodesFile).writeAsString(jsonStr);

      final corePath = await _getCorePath();
      _process = await Process.start(corePath, [
        'serve',
        '--nodes-file', nodesFile,
        '--listen', '127.0.0.1:$_listenPort',
      ]);

      _process!.stdout.transform(utf8.decoder).listen((line) {
        debugPrint('[core] $line');
      });
      _process!.stderr.transform(utf8.decoder).listen((line) {
        debugPrint('[core err] $line');
      });

      _isRunning = true;
      _status = 'running on :$_listenPort';
      notifyListeners();
    } catch (e) {
      _status = 'Error: $e';
      notifyListeners();
    }
  }

  Future<void> stopProxy() async {
    _process?.kill();
    _process = null;
    _isRunning = false;
    _status = 'stopped';
    notifyListeners();
  }
}
