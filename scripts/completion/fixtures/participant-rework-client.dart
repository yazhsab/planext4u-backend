import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:planext4u_api_client/planext4u_api_client.dart';
import 'package:planext4u_experience/planext4u_experience.dart';

class _Session implements ApiAuthSession {
  _Session(this.token);
  final String token;
  @override Future<String?> accessToken() async => token;
  @override Future<bool> refresh() async => false;
}
class _LostResponseTransport implements ApiTransport {
  final delegate = IoApiTransport();
  final keys = <String>[];
  bool lost = false, replayed = false;
  @override Future<TransportResponse> send(TransportRequest request, {required Duration timeout, required ApiCancellationToken cancellationToken}) async {
    final result = await delegate.send(request, timeout: timeout, cancellationToken: cancellationToken);
    if (request.url.path.endsWith('/resubmissions')) {
      keys.add(request.headers['Idempotency-Key']!);
      if (!lost && result.statusCode == 200) { lost = true; throw const SocketException('Synthetic response loss after owning commit'); }
      replayed = result.header('Idempotency-Replayed') == 'true';
    }
    return result;
  }
}
void main() {
  test('normal Flutter rework preserves acknowledged corrections across response loss', () async {
    final path = Platform.environment['PLANEXT_REVIEW_CONFIG'];
    if (path == null) throw StateError('Owned local rework fixture required.');
    final config = jsonDecode(await File(path).readAsString()) as Map<String, dynamic>;
    final vendorTransport = _LostResponseTransport(), riderTransport = _LostResponseTransport();
    ApiClient client(String token, ApiTransport transport) => ApiClient(baseUrl: Uri.parse('http://127.0.0.1:8082'), transport: transport, authSession: _Session(token), retryBudget: const ApiRetryBudget(maxAttempts: 2));
    final vendor = VendorOperationsApi(client(config['vendor_token'] as String, vendorTransport));
    final rejected = await vendor.application();
    expect(rejected.status, 'REJECTED'); expect(rejected.revision, 4);
    expect(rejected.allowedActions, contains('RESUBMIT_DOCUMENTS'));
    expect(rejected.timeline.last.reason, 'Synthetic local review rejection with recorded reason');
    final documents = (config['vendor_documents'] as List).cast<Map<String, dynamic>>();
    final correctedName = 'Corrected ${rejected.businessName}';
    final corrected = await vendor.resubmitApplication(
      rejected.revision,
      businessName: correctedName,
      businessType: 'PRIVATE_LIMITED',
      contactName: 'Corrected synthetic participant',
      documents: documents,
    );
    expect(corrected.status, 'DOCUMENTS_SUBMITTED'); expect(corrected.revision, 5); expect(corrected.verified, false);
    expect(corrected.businessName, correctedName); expect(corrected.businessType, 'PRIVATE_LIMITED');
    expect(corrected.bank, isNull); expect(corrected.fieldVisit, isNull); expect(corrected.zoneCount, 0);
    final vendorReadback = await vendor.application(); expect(vendorReadback.revision, 5); expect(vendorReadback.businessName, correctedName); expect(vendorReadback.allowedActions, isNot(contains('RESUBMIT_DOCUMENTS')));
    final rider = RiderOperationsApi(client(config['rider_token'] as String, riderTransport));
    final suspended = await rider.profile(); expect(suspended.status, 'SUSPENDED'); expect(suspended.reworkAllowed, true);
    expect(suspended.reviewReason, 'Synthetic local review rejection with recorded reason');
    expect(suspended.allowedActions, contains('RESUBMIT_APPLICATION'));
    final accepted = await rider.resubmit(suspended.revision, RiderRegistrationDraft(fullName: suspended.fullName, vehicleType: 'BICYCLE', vehicleNumber: suspended.vehicleNumber,
      documents: [RiderDocumentDraft(kind: 'IDENTITY', assetId: config['rider_document'] as String)], bankReference: config['rider_bank'] as String,
      zones: suspended.zones, dutyLocationConsent: true).toJson());
    expect(accepted.status, 'KYC_REVIEW'); expect(accepted.revision, 3); expect(accepted.reworkAllowed, false); expect(accepted.bankStatus, 'PENDING_VERIFICATION');
    expect((await rider.profile()).revision, 3);
    for (final transport in [vendorTransport, riderTransport]) {
      expect(transport.keys.length, 2); expect(transport.keys.toSet().length, 1); expect(transport.replayed, true);
      transport.delegate.close();
    }
  });
}
