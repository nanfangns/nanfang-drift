import 'package:flutter/material.dart';
import 'home_page.dart';

void main() {
  runApp(const NanfangApp());
}

class NanfangApp extends StatelessWidget {
  const NanfangApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Nanfang',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF1a1a2e)),
        useMaterial3: true,
        brightness: Brightness.dark,
      ),
      home: const HomePage(),
    );
  }
}
