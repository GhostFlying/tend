import { expect, test } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { MessagingDelivery } from "../api/generated/api.ts";
import { DeliveryReceipt } from "./Messaging.tsx";

test("delivery receipts keep their actual channel and opaque provider identity", () => {
  const html = renderToStaticMarkup(
    <>
      <DeliveryReceipt
        receipt={
          {
            channel_id: "feishu",
            state: "unknown",
            external_message_reference: "opaque-feishu-receipt",
          } as MessagingDelivery
        }
      />
      <DeliveryReceipt
        receipt={
          { channel_id: "telegram", state: "accepted", external_id: 42 } as MessagingDelivery
        }
      />
      <DeliveryReceipt receipt={{ channel_id: "web", state: "accepted" } as MessagingDelivery} />
      <DeliveryReceipt
        receipt={{ channel_id: "future-channel", state: "unknown" } as MessagingDelivery}
      />
    </>,
  );
  expect(html).toContain("飞书：");
  expect(html).toContain("回执：opaque-feishu-receipt");
  expect(html).toContain("Telegram：");
  expect(html).toContain("回执：42");
  expect(html).toContain("Web：");
  expect(html).toContain("future-channel：");
});
