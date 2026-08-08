package model

import (
	"server/globals"
	"time"
)

type Order struct {
	globals.GraMODEL
	UID             int       `json:"uid" gorm:"column:uid;default:0;index;comment:用户ID"`
	No              string    `json:"no" gorm:"column:no;type:varchar(32);default:'';uniqueIndex;comment:订单号"`
	PayMethod       string    `json:"payMethod" gorm:"column:pay_method;type:varchar(30);default:'0';comment:支付方式"`
	Amount          int       `json:"amount" gorm:"column:amount;not null;default:0;comment:价格"`
	Status          string    `json:"status" gorm:"column:status;type:varchar(32);default:'0';comment:状态"`
	ProductID       string    `json:"productId" gorm:"column:product_id;type:varchar(64);default:'';comment:产品ID(计划键)"`
	PayTime         time.Time `json:"payTime" gorm:"column:pay_time;default:null;comment:支付成功时间"`
	Name            string    `json:"name" gorm:"column:name;type:varchar(255);default:'';comment:订单名称"`
	IP              string    `json:"ip" gorm:"column:ip;type:varchar(255);comment:IP"`
	Invoice         string    `json:"invoice" gorm:"column:invoice;type:varchar(64);index;comment:stripe订阅模式发票编号"`
	ChargeID        string    `json:"chargeId" gorm:"column:charge_id;type:varchar(64);comment:stripe一次性模式退款编号"`
	TransID         string    `json:"transId" gorm:"column:trans_id;type:varchar(64);comment:stripe交易编号"`
	CustomerDetails string    `json:"customerDetails" gorm:"column:customer_details;type:text;comment:stripe客户详情"`
	OrderMode       string    `json:"orderMode" gorm:"column:order_mode;type:varchar(32);comment:订单模式"`
	SubscriptionID  string    `json:"subscriptionId" gorm:"column:subscription_id;type:varchar(64);index;comment:stripe订阅ID"`
	OrderType       string    `json:"orderType" gorm:"column:order_type;type:varchar(32);comment:订单类型"`
	CreditsAmount   int       `json:"creditsAmount" gorm:"column:credits_amount;default:0;comment:Credits数量"`
	// ProviderPriceID and the billing period are immutable provider facts frozen
	// on the Order. They keep webhook replay independent from mutable config and
	// let membership use Stripe's calendar boundary instead of repeatedly adding
	// a month to an already-clamped local timestamp.
	ProviderPriceID    string     `json:"providerPriceId" gorm:"column:provider_price_id;type:varchar(64);default:'';comment:支付服务商价格ID"`
	BillingPeriodStart *time.Time `json:"billingPeriodStart" gorm:"column:billing_period_start;default:null;comment:支付服务商账期开始"`
	BillingPeriodEnd   *time.Time `json:"billingPeriodEnd" gorm:"column:billing_period_end;default:null;comment:支付服务商账期结束"`
	// CheckoutSessionID is the durable bridge between the local unpaid Order and
	// the externally-created embedded Checkout Session. The client secret is not
	// persisted; a retry retrieves the same session by this provider identifier.
	CheckoutSessionID string `json:"-" gorm:"column:checkout_session_id;type:varchar(255);default:'';comment:Stripe Checkout Session ID"`
}

func (Order) TableName() string {
	return "w_order"
}

const (
	STATUS_UNPAID   = "UNPAID"
	STATUS_COMPLETE = "COMPLETE"
	STATUS_CANCEL   = "CANCEL"
	STATUS_REFUND   = "REFUND"
)

const (
	ORDER_MODE_SUBSCRIPTION        = "subscription"
	ORDER_MODE_ONE_TIME            = "one_time"
	ORDER_MODE_SUBSCRIPTION_UPDATE = "subscription_update"
)

const (
	ORDER_TYPE_MEMBER   = "member"
	ORDER_TYPE_AI_QUOTA = "aiquota"
	ORDER_TYPE_CREDITS  = "credits" // Credits 购买
)
