import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:planext4u_api_client/planext4u_api_client.dart';
import 'package:planext4u_experience/planext4u_experience.dart';

final class ReviewLiveSession implements ApiAuthSession {
  ReviewLiveSession(this.token);
  final String token;
  @override
  Future<String?> accessToken() async => token;
  @override
  Future<bool> refresh() async => false;
}

void main() {
  test(
    'normal vendor and rider adapters read the recorded admin decisions',
    () async {
      final path = Platform.environment['PLANEXT_REVIEW_CONFIG'];
      if (path == null)
        throw StateError('Owned local review fixture required.');
      final config =
          jsonDecode(await File(path).readAsString()) as Map<String, dynamic>;
      ApiClient client(String token) => ApiClient(
        baseUrl: Uri.parse('http://127.0.0.1:8082'),
        transport: IoApiTransport(),
        authSession: ReviewLiveSession(token),
        retryBudget: const ApiRetryBudget(maxAttempts: 1),
      );
      final vendor = await VendorOperationsApi(
        client(config['vendor_token'] as String),
      ).application();
      expect(vendor.status, 'REJECTED');
      expect(vendor.revision, 4);
      expect(vendor.allowedActions, isEmpty);
      expect(
        vendor.timeline.last.reason,
        'Synthetic local review rejection with recorded reason',
      );
      final rider = await RiderOperationsApi(
        client(config['rider_token'] as String),
      ).profile();
      expect(rider.status, 'SUSPENDED');
      expect(rider.revision, 2);
      expect(rider.allowedActions, isEmpty);
      expect(rider.bankStatus, 'PENDING_VERIFICATION');
    },
  );
}
