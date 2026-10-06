<!--
SPDX-License-Identifier: Apache-2.0
Licensed under the Apache License, Version 2.0.
https://www.apache.org/licenses/LICENSE-2.0
-->
# Mold Kubernetes 구성 요소 검증 후보

기준: Apache Provider main 2a46b8e43382bbd1564db7a9bfa56f9caa872d13. 내부 HMAC-SHA256 및 저장소 네임스페이스를 유지하면서 원본 변경을 병합했습니다.

Origin Actions에서 계약/기능 테스트 후 amd64 바이너리, 커밋 고정 이미지, Go 모듈 목록, provenance와 docker archive를 생성합니다. 공식 배포와 Origin 후보를 구분하며 ISO는 후보의 실제 digest와 source SHA를 lock에 기록합니다.

Provider는 최신 SDK 생성 API와 VPC ACL, 소스 CIDR, Proxy Protocol, 사용자 지정 LB IP의 소유권 정리, providerID, 영역/region, 페이지네이션 및 프로토콜 변경 동작을 포함합니다. source CIDR 업데이트는 Mold backend의 cidrlist 구현이 필요합니다.

후보 SDK를 Origin Go 모듈로 replace합니다. SDK 공식 PR 병합/릴리즈 후 공식 모듈 버전으로 전환해야 합니다. 1.34 실제 LB/VPC 런타임과 arm64는 단위 테스트만으로 PASS 판정하지 않습니다.

## TCP LoadBalancer backend 준비 확인

Mold Provider는 새 TCP backend를 연결하기 전에 검증된 VM의 주 guest NIC 주소와 서비스 NodePort로 TCP 연결을 확인합니다. Node Ready가 먼저 표시되어도 CNI/kube-proxy의 데이터 경로가 아직 준비되지 않은 경우 기존 정상 backend를 보존하고 controller가 재시도합니다. 연결 하나는 최대2초, 한 번의 준비 확인은 최대5초로 제한하며 불필요해진 backend 제거는 계속 처리합니다. UDP에는 TCP 검사를 적용하지 않습니다.

Controller가 guest network 밖에 있거나 egress 정책상 NodePort에 접근할 수 없는 배치에서는 Service annotation `service.beta.kubernetes.io/cloudstack-load-balancer-backend-readiness-check: "false"`로 명시적으로 해제할 수 있습니다. 기본값은 true이며 잘못된 boolean은 오류로 거부합니다. 이 검사는 Pod 애플리케이션의 HTTP readiness 검사를 대체하지 않습니다.

## VM 생명주기 상태 확인 (#1260)

CCM의 legacy/V2 InstanceShutdown은 Mold에서 안정적으로 Stopped인 VM에만 true를 반환한다.
Running 및 시작/중지/이동/삭제 중 상태는 안전한 디스크 분리 근거로 사용하지 않는다.
Error, Unknown 및 알 수 없는 상태는 오류를 반환하여 재확인을 요구한다.

InstanceExists는 성공한 listVirtualMachines 응답의 실제 빈 목록만 부재로 처리한다.
SDK의 GetVirtualMachineByID helper에서 count=0과 API 실패가 함께 반환될 수 있으므로,
인증/권한/전송/API 오류 또는 불완전·불일치 응답을 Node 삭제 근거로 사용하지 않는다.
Stopped VM은 존재하는 것으로 유지한다. V2는 providerID를 먼저 사용하고 미초기화 Node만
정확한 이름으로 조회하며 프로젝트 범위와 listAll을 보존한다. 이미 취소된 요청 및
취소 후 API 결과도 확정 상태로 사용하지 않는다. SDK HTTP 호출 자체의 중도 취소를
새로 지원한다는 뜻은 아니다.

#1260 후보는 내부 SDK v2.19.2-mold-test.2를 고정합니다. 이 SDK는 HTTP 200 API errorresponse 및 VM 조회의 잘못된 wrapper/null payload를 오류로 반환하므로 노드 부재로 처리되지 않습니다. Origin immutable 후보를 사용하며 실제 worker stop/recovery 검증 전 지원/PASS로 표시하지 않습니다.
